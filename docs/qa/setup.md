# QA setup and gates

Use a dedicated Linux/Docker stand, disposable PostgreSQL, test identities and synthetic data. Real IAM contract tests need Zitadel. Ordinary engine tests can use a deterministic provider and HTTP peers. Full integration needs the actual broker and client. Obtain explicit authorization for paid provider calls or destructive production tests.

Before a run, record the source SHA, dirty-source hashes, image IDs, schema version, sanitized settings and resource limits. Identify each owned container and volume. Preserve failures separately from successful repeats. A corrected observer does not make the first failed attempt pass.

Record PASS, FAIL, BLOCKED or SKIPPED with the exact scenario and expected/actual result. Do not infer acceptance from a skipped test or a test on another build. Store results outside source control.

## Code checks

Run from the repository root:

```sh
go mod verify
go test ./...
# Set CLX_TEST_DATABASE_DSN to a disposable, reachable PostgreSQL database.
sh scripts/linux-tests.sh
sh scripts/install-tools.sh
bin/golangci-lint config verify
bin/golangci-lint run --build-tags=integration ./...
bin/govulncheck ./...
docker build --platform linux/amd64 --target api -t loom:qa .
docker build --platform linux/amd64 --target file-worker -t loom:file-worker-qa .
```

The Linux script uses Docker; its DSN must be reachable from CLX_TEST_NETWORK. Without DSN it does not prove PostgreSQL integration. On Windows, use PowerShell-native environment assignment or run the shell commands from a supported shell.

The [CI workflow](../../.github/workflows/ci.yml) also tests pinned Poppler/FFmpeg in Linux. Ordinary tests without these CLIs do not accept the worker boundary. Consult [integration fixtures](../../integration/README.md) for the fake authority/provider limits.

For a PR, run an independent review loop before publication, wait for CI, and resolve valid reviewer/bot findings. Use [Contributing](../../CONTRIBUTING.md) for documentation requirements.
