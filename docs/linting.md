# Go linting

Use golangci-lint **v2.14.0**. The project configuration is based on the
[Golden configuration](https://github.com/maratori/golangci-lint-config/blob/b57a98e8ac187b8c401660d2205caa6523bd86fb/.golangci.yml)
at commit `b57a98e8ac187b8c401660d2205caa6523bd86fb`, checked on 2026-10-11.
The upstream MIT license notice remains in `.golangci.yml`.

The configuration enables all 79 Golden linters and retains `contextcheck`,
the existing project check for inherited request contexts: 80 linters in total.
It also enables the Golden `goimports` and `golines` formatters. Imports from
`github.com/complynx/loom` form the local import group, and the target
maximum line length is 120.

The project retains the ban on `golang.org/x/crypto/openpgp`, a five minute
timeout, and unlimited issue output. Other settings and exclusions come from
Golden. In particular, Golden excludes selected checks in test files, while
keeping test assertions, parallel execution, and test package checks enabled.

CI verifies the configuration and lints integration-tagged sources as well.
Tests use isolated database schemas; `-parallel=4` bounds concurrent fixtures.

Run checks from the repository root:

```sh
golangci-lint config verify
golangci-lint run --build-tags=integration ./...
golangci-lint fmt --diff ./cmd/... ./internal/... ./integration/...
```

To apply the configured formatters:

```sh
golangci-lint fmt ./cmd/... ./internal/... ./integration/...
```

Use these explicit source directories when formatting. The dependency license
archive under `docs/dependency-audit` contains upstream Go source files whose
original bytes must be preserved.

## Nebius Go SDK comparison

The [Nebius Go SDK configuration](https://github.com/nebius/gosdk/blob/b3d77e844e6214dfebf02f820adf8f20e0c05e80/.golangci.yml),
at commit `b3d77e844e6214dfebf02f820adf8f20e0c05e80`, was checked on the same date.
It also derives from Golden and enables 78 linters. Its enabled list matches
Golden except for `embeddedstructfieldcheck`, which Nebius leaves optional.
Both enable `goimports` and `golines`, with a 120 character target line length.

Nebius adapts the local import prefix and adds exclusions for its field mask,
SDK generation, authentication, and configuration code. Those project specific
exclusions are not used here. This repository uses the current Golden settings
and its own existing context and deprecated cryptography checks.

Run lint alongside the behavioral gates in [QA](qa.md).

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
