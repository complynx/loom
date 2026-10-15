#!/usr/bin/env python3
"""Generate or verify the current Linux dependency license snapshot."""
import argparse
import csv
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
CURRENT = ROOT / "docs/dependency-audit/current"
ALLOWED = {"MIT", "Apache-2.0", "BSD-2-Clause", "BSD-3-Clause", "MPL-2.0", "ISC", "Unlicense", "LicenseRef-MIT-Lucent"}


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def selected(lock):
    return sorted(({k: m[k] for k in ("module", "version", "commit")} for m in lock["modules"]), key=lambda m: m["module"])


def run(*args):
    env = os.environ.copy()
    env.update(GOOS="linux", GOARCH="amd64", CGO_ENABLED="0", GOTOOLCHAIN="local", GOWORK="off", GOENV="off")
    env.pop("GH_TOKEN", None)
    env.pop("GITHUB_TOKEN", None)
    result = subprocess.run(args, cwd=ROOT, env=env, text=True, capture_output=True)
    if result.returncode:
        raise SystemExit(f"{args[0]} failed:\n{result.stderr.strip()}")
    return result.stdout


def check_reports(directory):
    for name in ("licenses-linux.csv", "licenses-linux-tests.csv"):
        rows = list(csv.reader((directory / name).read_text(encoding="utf-8").splitlines()))
        if not rows or any(len(row) != 3 or row[2] not in ALLOWED for row in rows):
            raise SystemExit(f"Manual license review required: {name}")


def verify():
    if CURRENT.is_symlink() or any(p.is_symlink() for p in CURRENT.rglob("*")):
        raise SystemExit("Symlink in committed dependency audit")
    lock = json.loads((ROOT / "tools/dependencies.lock.json").read_text(encoding="utf-8"))
    manifest = json.loads((CURRENT / "manifest.json").read_text(encoding="utf-8"))
    if manifest["modules"] != selected(lock) or manifest["go"] != lock["go"]["version"]:
        raise SystemExit("Dependency audit is stale; run make audit-update after lock-update")
    expected = {f["path"]: f["sha256"] for f in manifest["files"]}
    actual = {p.relative_to(CURRENT).as_posix(): digest(p) for p in CURRENT.rglob("*") if p.is_file() and p != CURRENT / "manifest.json"}
    if actual != expected:
        raise SystemExit("Dependency audit content differs from its byte manifest")
    check_reports(CURRENT)
    print(f"verified current audit for {len(manifest['modules'])} pinned modules")


def root_licenses(lock):
    known = {}
    for pattern in ("*/module-root-licenses.json", "*/module-license-delta.json"):
        for report in (ROOT / "docs/dependency-audit").glob(pattern):
            for module in json.loads(report.read_text(encoding="utf-8")):
                for item in module["license_files"]:
                    labels = item.get("licenses", [])
                    names = {label["Name"] if isinstance(label, dict) else label for label in labels}
                    # Root snapshots include unshipped docs/logo exceptions.
                    # Reuse reviewed bytes only; runtime/test reports enforce ALLOWED separately.
                    known[item["sha256"]] = sorted(names)
    result = []
    for module in lock["modules"]:
        metadata = json.loads(run("go", "mod", "download", "-json", module["module"] + "@" + module["version"]))
        files = []
        for path in sorted(Path(metadata["Dir"]).iterdir()):
            if not path.is_file() or path.suffix.lower() == ".go" or not path.name.upper().startswith(("LICENSE", "LICENCE", "UNLICENSE", "COPYING")):
                continue
            sha = digest(path)
            if sha not in known:
                raise SystemExit(f"Manual root-license review required: {module['module']} {path.name} sha256={sha}")
            files.append({"path": path.name, "sha256": sha, "licenses": known[sha]})
        if not files:
            raise SystemExit("Missing root license: " + module["module"])
        result.append({"module": module["module"], "version": module["version"], "license_files": files})
    return result


def notice_boundary(directory, version):
    notices = directory / "notices"
    notices.mkdir(parents=True, exist_ok=True)
    # Archived MPL sources must never become packages in go test ./....
    (notices / "go.mod").write_text("module loom.invalid/dependency-notices\n\ngo " + version.removeprefix("go") + "\n", encoding="utf-8")


def update():
    lock = json.loads((ROOT / "tools/dependencies.lock.json").read_text(encoding="utf-8"))
    scanner = os.environ.get("GO_LICENSES_BINARY", "go-licenses")
    with tempfile.TemporaryDirectory(prefix="loom-license-") as temporary:
        temporary = Path(temporary)
        stage = temporary / "stage"
        stage.mkdir()
        for name, flags in (("licenses-linux.csv", []), ("licenses-linux-tests.csv", ["--include_tests"])):
            report = run(scanner, "report", *flags, "./cmd/...", "./internal/...", "./integration/...")
            (stage / name).write_text(report, encoding="utf-8", newline="\n")
        check_reports(stage)
        saved = temporary / "saved"
        run(scanner, "save", "--ignore", "github.com/dop251/goja/ftoa", "--save_path", str(saved), "./cmd/loom", "./internal/...")
        goja = json.loads(run("go", "list", "-m", "-json", "github.com/dop251/goja"))["Dir"]
        for suffix in ("ftoa/LICENSE_LUCENE", "ftoa/internal/fast/LICENSE_V8"):
            target = saved / "github.com/dop251/goja" / suffix
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(Path(goja) / suffix, target)
        stdlib = saved / "go.std/LICENSE"
        stdlib.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(Path(run("go", "env", "GOROOT").strip()) / "LICENSE", stdlib)
        baseline = [ROOT / "docs/dependency-audit" / name / "notices" for name in ("2026-10-11-mvp", "2026-10-12-otel")]
        for path in sorted(saved.rglob("*")):
            if path.is_symlink():
                raise SystemExit("Symlink in generated license notices")
            if not path.is_file():
                continue
            relative = path.relative_to(saved)
            if any((base / relative).is_file() and digest(base / relative) == digest(path) for base in baseline):
                continue
            target = stage / "notices" / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(path, target)
        roots = root_licenses(lock)
        notice_boundary(stage, lock["go"]["version"])
        (stage / "module-root-licenses.json").write_text(json.dumps(roots, indent=2) + "\n", encoding="utf-8")
        manifest = {"go": lock["go"]["version"], "modules": selected(lock), "files": [
            {"path": p.relative_to(stage).as_posix(), "sha256": digest(p)} for p in sorted(stage.rglob("*")) if p.is_file()
        ]}
        (stage / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
        if CURRENT.is_symlink() or CURRENT.resolve().parent != (ROOT / "docs/dependency-audit").resolve():
            raise SystemExit("Unexpected generated-audit destination")
        if CURRENT.exists():
            shutil.rmtree(CURRENT)
        shutil.copytree(stage, CURRENT)
    verify()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    modes = parser.add_mutually_exclusive_group(required=True)
    modes.add_argument("--check", action="store_true")
    modes.add_argument("--update", action="store_true")
    args = parser.parse_args()
    update() if args.update else verify()
