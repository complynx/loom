# Go linting

Use the golangci-lint version pinned in [scripts/install-tools.sh](../scripts/install-tools.sh).
The current [.golangci.yml](../.golangci.yml) targets v2.14.0 and derives from the
[Golden configuration at its reviewed commit](https://github.com/maratori/golangci-lint-config/blob/b57a98e8ac187b8c401660d2205caa6523bd86fb/.golangci.yml).
Its upstream MIT notice remains in the configuration file.

## Check source

Run from the repository root with the Go version in [go.mod](../go.mod) and a POSIX
shell. Installation needs network access. The installer defaults to `./bin`;
unset an existing `GOBIN` override or adjust the paths below to match it.

```sh
sh scripts/install-tools.sh
bin/golangci-lint config verify
bin/golangci-lint run --build-tags=integration ./...
bin/golangci-lint fmt --diff ./cmd/... ./internal/... ./integration/...
```

On Windows, the installed executable is `bin/golangci-lint.exe`. The integration
build tag includes integration source in lint analysis; lint does not execute
tests or require a test database.

To apply the configured formatters, then check the result:

```sh
bin/golangci-lint fmt ./cmd/... ./internal/... ./integration/...
bin/golangci-lint run --build-tags=integration ./...
git diff --check
```

Use these explicit source directories when formatting. The dependency license
archive under `docs/dependency-audit` contains upstream Go source whose original
bytes must be preserved. Do not run a repository-wide formatter over the archive.

## Project settings

The configuration enables the Golden linter set plus `contextcheck`, which checks
inherited request contexts. It enables `goimports` and `golines`, groups imports
from `github.com/complynx/loom` as local imports and targets 120-character lines.
It also retains the `golang.org/x/crypto/openpgp` ban, a five-minute timeout and
unlimited issue output.

Selected checks are excluded in test files; test assertions, parallel execution
and test package checks remain enabled. Read the configuration before adding a
suppression. Keep any suppression narrow and explain the reason when required.

[CI](../.github/workflows/ci.yml) verifies the configuration and lints integration
sources. Run the behavioral gates in [QA setup](qa/setup.md) separately.
