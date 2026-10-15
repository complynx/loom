#!/usr/bin/env python3
"""Verify module versions against canonical VCS origins; update after review."""
import argparse
import json
import os
import tempfile
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[1]
LOCK = ROOT / "tools" / "dependencies.lock.json"


def go_json(*args, env=None):
    try:
        result = subprocess.run(["go", *args], cwd=ROOT, check=True, text=True, capture_output=True, env=env)
    except subprocess.CalledProcessError as error:
        raise SystemExit(f"go {' '.join(args)} failed:\n{error.stderr.strip()}") from error
    decoder = json.JSONDecoder()
    tail = result.stdout.strip()
    records = []
    while tail:
        item, end = decoder.raw_decode(tail)
        records.append(item)
        tail = tail[end:].lstrip()
    return records


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    parser.add_argument("--update", action="store_true")
    args = parser.parse_args()
    if args.check == args.update:
        parser.error("choose --check or --update")
    lock = json.loads(LOCK.read_text(encoding="utf-8"))
    pins = {item["module"]: item for item in lock["modules"]}
    actual = []
    for module in go_json("list", "-m", "-json", "all"):
        if module.get("Main"):
            continue
        if "Replace" in module:
            raise SystemExit(f"module replacement requires manual review: {module['Path']}")
        pinned = pins.get(module["Path"])
        if pinned and pinned["version"] == module["Version"]:
            commit = pinned["commit"]
            if not re.fullmatch(r"[0-9a-f]{40}", commit):
                raise SystemExit(f"invalid complete commit pin: {module['Path']}")
            resolved = go_json("list", "-m", "-json", f"{module['Path']}@{commit}")[0]
            if resolved["Version"] != module["Version"]:
                raise SystemExit(f"commit resolves to wrong version: {module['Path']}")
            record = dict(pinned)
            record["indirect"] = module.get("Indirect", False)
            actual.append(record)
            continue
        if args.check:
            raise SystemExit(f"missing or outdated pin: {module['Path']}@{module['Version']}")
        # VCS origin metadata must come from Go, never infer a commit from a version.
        downloaded = go_json("mod", "download", "-json", f"{module['Path']}@{module['Version']}")[0]
        origin = downloaded.get("Origin", {})
        commit = origin.get("Hash", "")
        if not re.fullmatch(r"[0-9a-f]{40}", commit):
            # Old proxy caches may omit Origin. Resolve metadata through VCS in
            # an isolated empty cache, so an old .info file cannot mask it.
            with tempfile.TemporaryDirectory(prefix="clx-module-origin-") as cache:
                environment = os.environ.copy()
                environment.update(GOPROXY="direct", GOMODCACHE=cache, GOWORK="off")
                resolved = go_json("list", "-m", "-json", f"{module['Path']}@{module['Version']}", env=environment)[0]
                origin = resolved.get("Origin", {})
                commit = origin.get("Hash", "")
        if not re.fullmatch(r"[0-9a-f]{40}", commit):
            raise SystemExit(f"canonical VCS origin unavailable: {module['Path']}")
        verified = go_json("list", "-m", "-json", f"{module['Path']}@{commit}")[0]
        if verified["Version"] != module["Version"]:
            raise SystemExit(f"origin commit does not resolve to locked version: {module['Path']}")
        actual.append({"module": module["Path"], "version": module["Version"], "commit": commit,
                       "repository": origin["URL"], "indirect": module.get("Indirect", False)})
    actual.sort(key=lambda item: item["module"])
    expected = sorted(lock["modules"], key=lambda item: item["module"])
    if args.check:
        if actual != expected:
            raise SystemExit("dependency lock differs: review go.mod/go.sum, then run make lock-update")
        print(f"verified {len(actual)} complete module commit pins")
    else:
        lock["modules"] = actual
        LOCK.write_text(json.dumps(lock, indent=2) + "\n", encoding="utf-8")
        print(f"updated {len(actual)} module commit pins; review before committing")


if __name__ == "__main__":
    main()
