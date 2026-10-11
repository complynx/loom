# Loom pre-commit verification — 2026-10-12

Project codename: Loom. Branch: codex/loom. Technical executable, module path, container names and API labels remain clx-scripter for compatibility.

All checks passed after consolidating application migrations:

- Windows: `go test -count=1 -parallel=4 -tags=integration ./...`, exit 0, isolated PostgreSQL.
- Linux/Docker: `go test -count=1 -race -parallel=4 -tags=integration ./...`, exit 0, isolated PostgreSQL.
- Golden: `golangci-lint run --build-tags=integration ./...`, exit 0, 0 issues.
- Docker Linux image build: exit 0; local tag clx-scripter:loom-precommit-20261012. No image was published by this check.
- [Schema equivalence and SQL rollback/reapply](schema-check.txt): legacy 12-step application schema matches baseline; Down removes the application tables and Up restores the same schema. Goose Up was exercised by integration tests. River migrations remain independent.
- Commit candidates were scanned for full JWT signatures, OpenAI keys and private PEM keys: no matching files; no executable/archive/key/env candidates. This is a pattern audit, not a universal secret detector.

Baseline is for a new database. Do not migrate old 00001–00012 QA/dev databases in place or edit their Goose ledger. Retain them with the old reviewed image or use a separately verified data transition. No existing QA or production data was removed. Private stands, credentials, scratch references and commit message remain ignored in management.local.

Historical QA retains its original images, source manifests, observed failures and boundaries. The pre-commit gates do not replace production acceptance or live provider quality checks.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
