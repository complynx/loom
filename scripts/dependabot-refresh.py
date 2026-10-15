#!/usr/bin/env python3
"""Refresh metadata using trusted base scripts; never execute the PR checkout."""
import argparse
import base64
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import urllib.error
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
STATE = Path(os.environ.get("RUNNER_TEMP", str(ROOT / "management.local"))) / "loom-dependabot-state.json"


def api(method, path, body=None):
    request = urllib.request.Request("https://api.github.com" + path, method=method,
        data=json.dumps(body).encode() if body is not None else None,
        headers={"Authorization": "Bearer " + os.environ["GH_TOKEN"], "Accept": "application/vnd.github+json", "Content-Type": "application/json", "X-GitHub-Api-Version": "2022-11-28"})
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            data = response.read(8 << 20)
            return json.loads(data) if data else None
    except urllib.error.HTTPError as error:
        raise SystemExit(f"GitHub {method} {path} failed: HTTP {error.code}") from error


def allowed(path):
    if "\\" in path or path.startswith("/") or any(part in (".", "..") for part in path.split("/")):
        return False
    return path in ("go.mod", "go.sum", "tools/dependencies.lock.json")


def validate_pr(pr, repository, paths):
    if pr["state"] != "open" or pr["user"]["login"] != "dependabot[bot]":
        raise ValueError("Open Dependabot PR required")
    if pr["base"]["repo"]["full_name"] != repository or pr["head"]["repo"]["full_name"] != repository:
        raise ValueError("Same-repository PR required")
    if not pr["head"]["ref"].startswith("dependabot/go_modules/") or not re.fullmatch(r"[0-9a-f]{40}", pr["head"]["sha"]):
        raise ValueError("Unsupported dependency branch")
    if not paths or "go.mod" not in paths or not all(allowed(path) for path in paths):
        raise ValueError("PR contains unsupported file changes; manual refresh required")


def go_module(content):
    env = os.environ.copy()
    env.update(GOTOOLCHAIN="local", GOWORK="off", GOENV="off")
    env.pop("GH_TOKEN", None)
    env.pop("GITHUB_TOKEN", None)
    # Parse data only with the installed Go binary. No PR scripts or package code run.
    with tempfile.TemporaryDirectory(prefix="loom-go-mod-") as directory:
        path = Path(directory) / "go.mod"
        path.write_bytes(content)
        result = subprocess.run(["go", "mod", "edit", "-json", str(path)], cwd=directory, env=env, capture_output=True, text=True, check=True)
        return json.loads(result.stdout)


def validate_modules(before, after):
    if before["Module"] != after["Module"] or before["Go"] != after["Go"]:
        raise ValueError("Module identity/toolchain changes require manual review")
    for directive in ("Replace", "Exclude", "Retract", "Tool", "Toolchain", "Godebug"):
        if after.get(directive):
            raise ValueError(f"Unsupported go.mod directive: {directive}")


def context():
    repository = os.environ["GITHUB_REPOSITORY"]
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository):
        raise ValueError("Invalid repository")
    number = int(os.environ["PR_NUMBER"])
    base = "/repos/" + repository
    return repository, number, base


def prepare():
    repository, number, base = context()
    pr = api("GET", f"{base}/pulls/{number}")
    paths = []
    for page in range(1, 12):
        files = api("GET", f"{base}/pulls/{number}/files?per_page=100&page={page}")
        paths.extend(file["filename"] for file in files)
        paths.extend(file["previous_filename"] for file in files if "previous_filename" in file)
        if len(files) < 100:
            break
    else:
        raise ValueError("PR is too large for automatic refresh")
    validate_pr(pr, repository, paths)
    checkout = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
    if checkout != pr["base"]["sha"]:
        raise ValueError("Base changed; rerun against its current trusted revision")
    incoming = {}
    for path in ("go.mod", "go.sum"):
        file = api("GET", f"{base}/contents/{path}?ref={pr['head']['sha']}")
        if file["type"] != "file" or file["encoding"] != "base64" or file["size"] > 1 << 20:
            raise ValueError("Unsupported module file")
        incoming[path] = base64.b64decode(file["content"], validate=False)
    validate_modules(go_module((ROOT / "go.mod").read_bytes()), go_module(incoming["go.mod"]))
    for path, data in incoming.items():
        (ROOT / path).write_bytes(data)
    STATE.parent.mkdir(parents=True, exist_ok=True)
    STATE.write_text(json.dumps({"head": pr["head"]["sha"], "base": pr["base"]["sha"], "branch": pr["head"]["ref"], "number": number, "repository": repository}), encoding="utf-8")


def changes():
    tracked = subprocess.check_output(["git", "diff", "--name-only", "-z"], cwd=ROOT).decode().split("\0")
    untracked = subprocess.check_output(["git", "ls-files", "--others", "--exclude-standard", "-z"], cwd=ROOT).decode().split("\0")
    paths = set(tracked + untracked) - {""}
    # Include the complete pin state, even when it matches the trusted base.
    paths.update(("go.mod", "go.sum", "tools/dependencies.lock.json"))
    paths = sorted(paths)
    if len(paths) > 1000 or any(not allowed(path) for path in paths):
        raise ValueError("Unexpected generated paths")
    for path in paths:
        file = ROOT / path
        if file.is_symlink() or not file.resolve().is_relative_to(ROOT.resolve()):
            raise ValueError("Generated path escapes checkout")
        if file.exists() and (not file.is_file() or file.stat().st_size > 1 << 20):
            raise ValueError("Unsupported generated file")
    if sum((ROOT / path).stat().st_size for path in paths if (ROOT / path).exists()) > 20 << 20:
        raise ValueError("Generated metadata exceeds size limit")
    return paths


def publish():
    repository, number, base = context()
    state = json.loads(STATE.read_text(encoding="utf-8"))
    pr = api("GET", f"{base}/pulls/{number}")
    validate_pr(pr, repository, ["go.mod"])
    if state != {"head": pr["head"]["sha"], "base": pr["base"]["sha"], "branch": pr["head"]["ref"], "number": number, "repository": repository}:
        raise ValueError("PR changed during refresh; do not publish stale metadata")
    commit = api("GET", f"{base}/git/commits/{state['head']}")
    previous = api("GET", f"{base}/git/trees/{commit['tree']['sha']}?recursive=1")
    if previous.get("truncated"):
        raise ValueError("PR tree is too large for safe metadata replacement")
    previous_paths = {item["path"] for item in previous["tree"] if item["type"] == "blob" and allowed(item["path"])}
    paths = set(changes()) | previous_paths
    tree = []
    for path in sorted(paths):
        file = ROOT / path
        if not file.exists() and path not in previous_paths:
            continue
        item = {"path": path, "mode": "100644", "type": "blob", "sha": None}
        if file.exists():
            blob = api("POST", base + "/git/blobs", {"encoding": "base64", "content": base64.b64encode(file.read_bytes()).decode()})
            item["sha"] = blob["sha"]
        tree.append(item)
    updated = api("POST", base + "/git/trees", {"base_tree": commit["tree"]["sha"], "tree": tree})
    if updated["sha"] != commit["tree"]["sha"]:
        new = api("POST", base + "/git/commits", {"message": "chore(deps): refresh dependency commit pins\n\nWritten by Loom dependency automation (GitHub Actions)\non behalf of Daniel Drizhuk", "tree": updated["sha"], "parents": [state["head"]]})
        branch = urllib.parse.quote(state["branch"], safe="/")
        api("PATCH", f"{base}/git/refs/heads/{branch}", {"sha": new["sha"], "force": False})
    # GITHUB_TOKEN pushes do not trigger push/PR workflows. Explicitly run the same full CI.
    api("POST", base + "/actions/workflows/ci.yml/dispatches", {"ref": state["branch"]})


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("prepare", "publish"))
    args = parser.parse_args()
    try:
        prepare() if args.mode == "prepare" else publish()
    except (ValueError, subprocess.CalledProcessError) as error:
        raise SystemExit(str(error)) from error
