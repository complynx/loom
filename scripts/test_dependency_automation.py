"""Behavioral gates for metadata refresh authority, races and license checks."""
import base64
import contextlib
import copy
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import subprocess
import unittest
from unittest.mock import patch


def load(name):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).parent / (name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


refresh = load("dependabot-refresh")
audit = load("dependency-audit")


def pr():
    return {"state": "open", "user": {"login": "dependabot[bot]"},
        "head": {"sha": "a" * 40, "ref": "dependabot/go_modules/example-1.2.0", "repo": {"full_name": "complynx/loom"}},
        "base": {"sha": "b" * 40, "repo": {"full_name": "complynx/loom"}}}


class AuthorityTests(unittest.TestCase):
    def test_only_same_repo_dependabot_module_changes_are_accepted(self):
        refresh.validate_pr(pr(), "complynx/loom", ["go.mod", "go.sum"])
        cases = []
        fork = pr()
        fork["head"]["repo"]["full_name"] = "attacker/loom"
        cases.append((fork, ["go.mod"]))
        user = pr()
        user["user"]["login"] = "someone"
        cases.append((user, ["go.mod"]))
        cases.extend((pr(), ["go.mod", path]) for path in ("scripts/dependency-lock.py", ".github/workflows/ci.yml", "docs/dependency-audit/current/manifest.json", "docs/dependency-audit/current/../../scripts/evil.py", "/go.sum"))
        for candidate, paths in cases:
            with self.subTest(paths=paths, author=candidate["user"]):
                with self.assertRaises(ValueError):
                    refresh.validate_pr(candidate, "complynx/loom", paths)

    def test_module_parser_allows_versions_but_rejects_local_replacements(self):
        before = refresh.go_module(b"module example.test/service\ngo 1.21\nrequire example.test/lib v1.0.0\n")
        after = refresh.go_module(b"module example.test/service\ngo 1.21\nrequire example.test/lib v1.1.0\n")
        refresh.validate_modules(before, after)
        replaced = refresh.go_module(b"module example.test/service\ngo 1.21\nreplace example.test/lib => /tmp/untrusted\n")
        with self.assertRaises(ValueError):
            refresh.validate_modules(before, replaced)
        after["Go"] = "1.99"
        with self.assertRaises(ValueError):
            refresh.validate_modules(before, after)


class RefreshFlowTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.state = self.root / "state.json"
        self.current = pr()
        self.calls = []
        self.mod = b"module example.test/service\ngo 1.21\nrequire example.test/lib v1.1.0\n"
        (self.root / "go.mod").write_bytes(b"module example.test/service\ngo 1.21\nrequire example.test/lib v1.0.0\n")
        self.files = [{"filename": "go.mod"}, {"filename": "go.sum"}]
        self.tree_sha = "new-tree"
        self.previous_tree = []
        for name, value in (("ROOT", self.root), ("STATE", self.state)):
            mock = patch.object(refresh, name, value)
            mock.start()
            self.addCleanup(mock.stop)
        environment = patch.dict(os.environ, {"GITHUB_REPOSITORY": "complynx/loom", "PR_NUMBER": "12"})
        environment.start()
        self.addCleanup(environment.stop)
        network = patch.object(refresh, "api", side_effect=self.api)
        network.start()
        self.addCleanup(network.stop)

    def api(self, method, path, body=None):
        self.calls.append((method, path, copy.deepcopy(body)))
        if path.endswith("/pulls/12"):
            return copy.deepcopy(self.current)
        if "/files?" in path:
            return self.files
        if "/contents/" in path:
            data = self.mod if "/go.mod?" in path else b""
            return {"type": "file", "encoding": "base64", "size": len(data), "content": base64.b64encode(data).decode()}
        if method == "GET" and "/git/commits/" in path:
            return {"tree": {"sha": "old-tree"}}
        if method == "GET" and "/git/trees/" in path:
            return {"tree": self.previous_tree, "truncated": False}
        if path.endswith("/git/blobs"):
            return {"sha": "blob"}
        if path.endswith("/git/trees"):
            return {"sha": self.tree_sha}
        if path.endswith("/git/commits"):
            return {"sha": "c" * 40}
        if method == "PATCH" or path.endswith("/dispatches"):
            return None
        raise AssertionError((method, path))

    def prepare(self):
        with patch.object(refresh.subprocess, "check_output", return_value="b" * 40 + "\n"):
            refresh.prepare()

    def test_refresh_preserves_pr_parent_and_runs_full_ci_on_new_head(self):
        self.prepare()
        self.assertEqual((self.root / "go.mod").read_bytes(), self.mod)
        with patch.object(refresh, "changes", return_value=["go.mod"]):
            refresh.publish()
        commit = next(body for method, path, body in self.calls if method == "POST" and path.endswith("/git/commits"))
        self.assertEqual(commit["parents"], ["a" * 40])
        update = next(body for method, _, body in self.calls if method == "PATCH")
        self.assertFalse(update["force"])
        self.assertEqual(update["sha"], "c" * 40)
        dispatch = next(body for _, path, body in self.calls if path.endswith("/dispatches"))
        self.assertEqual(dispatch["ref"], self.current["head"]["ref"])

    def test_changed_head_or_base_is_not_overwritten(self):
        for part in ("head", "base"):
            with self.subTest(part=part):
                self.current = pr()
                self.prepare()
                self.calls.clear()
                self.current[part]["sha"] = "d" * 40
                with self.assertRaises(ValueError):
                    refresh.publish()
                self.assertFalse(any(method != "GET" for method, _, _ in self.calls))

    def test_already_current_metadata_does_not_create_duplicate_commit(self):
        self.prepare()
        self.tree_sha = "old-tree"
        with patch.object(refresh, "changes", return_value=[]):
            refresh.publish()
        self.assertFalse(any(method == "PATCH" for method, _, _ in self.calls))
        self.assertTrue(any(path.endswith("/dispatches") for _, path, _ in self.calls))

    def test_renaming_trusted_script_into_metadata_does_not_pass(self):
        self.files.append({"filename": "docs/dependency-audit/current/injected.py", "previous_filename": "scripts/dependency-lock.py"})
        before = (self.root / "go.mod").read_bytes()
        with self.assertRaises(ValueError):
            self.prepare()
        self.assertEqual((self.root / "go.mod").read_bytes(), before)

    def test_pin_refresh_preserves_manual_license_snapshot_in_pr_tree(self):
        self.prepare()
        notice = "docs/dependency-audit/current/notices/lib/LICENSE"
        file = self.root / notice
        file.parent.mkdir(parents=True)
        file.write_bytes(b"older reviewed snapshot")
        self.previous_tree = [{"path": notice, "type": "blob"}]
        with patch.object(refresh.subprocess, "check_output", return_value=b""):
            refresh.publish()
        updated = next(body for method, path, body in self.calls if method == "POST" and path.endswith("/git/trees"))
        self.assertEqual(updated["base_tree"], "old-tree")
        self.assertFalse(any(item["path"].startswith("docs/") for item in updated["tree"]))


class AuditTests(unittest.TestCase):
    def test_archived_sources_are_not_project_test_packages(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "go.mod").write_text("module example.test/project\n\ngo 1.21\n")
            (root / "main.go").write_text("package main\nfunc main() {}\n")
            current = root / "docs/dependency-audit/current"
            audit.notice_boundary(current, "go1.21")
            (current / "notices/archived.go").write_text("This is deliberately invalid Go.\n")
            result = subprocess.run(["go", "list", "./..."], cwd=root, text=True, capture_output=True, check=True)
            self.assertEqual(result.stdout.strip(), "example.test/project")

    def test_check_rejects_stale_graph_modified_notices_and_forbidden_license(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            current = root / "docs/dependency-audit/current"
            current.mkdir(parents=True)
            (root / "tools").mkdir()
            lock = {"go": {"version": "go1.27.2"}, "modules": [{"module": "example.test/lib", "version": "v1.0.0", "commit": "a" * 40}]}
            (root / "tools/dependencies.lock.json").write_text(json.dumps(lock))
            for name in ("licenses-linux.csv", "licenses-linux-tests.csv"):
                (current / name).write_text("example.test/lib,https://example.test/LICENSE,MIT\n")
            def manifest():
                files = [{"path": p.name, "sha256": audit.digest(p)} for p in current.glob("*.csv")]
                (current / "manifest.json").write_text(json.dumps({"go": "go1.27.2", "modules": audit.selected(lock), "files": files}))
            manifest()
            with patch.object(audit, "ROOT", root), patch.object(audit, "CURRENT", current), contextlib.redirect_stdout(io.StringIO()):
                audit.verify()
                report = current / "licenses-linux.csv"
                report.write_text("modified")
                with self.assertRaises(SystemExit):
                    audit.verify()
                report.write_text("example.test/lib,https://example.test/LICENSE,GPL-3.0\n")
                manifest()
                with self.assertRaises(SystemExit):
                    audit.verify()
                report.write_text("example.test/lib,https://example.test/LICENSE,MIT\n")
                manifest()
                lock["modules"][0]["version"] = "v1.1.0"
                (root / "tools/dependencies.lock.json").write_text(json.dumps(lock))
                with self.assertRaises(SystemExit):
                    audit.verify()


if __name__ == "__main__":
    unittest.main()
