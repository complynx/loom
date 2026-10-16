# Contributing to Loom

## Read the system before changing it

Start with the [README](README.md), [architecture](docs/architecture.md) and [limitations](docs/mvp-limitations.md). Read the contract for the boundary you change. The [development track](docs/development-track.md) separates existing implementation, missing acceptance and new work.

A run is one durable task in a chat. Its status, inputs, steps and effects survive worker restarts. A user has one default chat. A service actor can act for a user only through an authorized caller binding and delegation. Model output is not authority to bypass these checks.

## Code map

| Path | Responsibility |
| --- | --- |
| cmd/loom | CLI entry points and process startup. |
| internal/app | Runtime assembly, tool registration, built-in help and module bridge. |
| internal/api | HTTP/OpenAPI contracts, input/control endpoints and operator panel. |
| internal/domain | Shared domain types. |
| internal/runs | Agent loop, model/tool steps, steering, compaction and checkpoints. |
| internal/store | PostgreSQL transactions, leases, fencing, history and billing persistence. |
| internal/identity | JWT/JWKS, introspection, delegated sessions and permission cache. |
| internal/modules | Manifest validation, discovery, permission-filtered methods and RPC. |
| internal/operations | Prepare/confirm/execute and recovery of external writes. |
| internal/provider, internal/transcription | Model and transcription transports and usage. |
| internal/sandbox | Supervisor, workers and script IPC. |
| internal/artifacts, internal/fileproc | Temporary files, handles, source references and isolated file jobs. |
| internal/history, internal/lifecycle, internal/telemetry | Context/history helpers, lifecycle and observability. |
| migrations | Application schema and embedded migrations. River has separate migration history. |
| integration | Behavioral tests with disposable PostgreSQL and explicit boundary fakes. |
| deploy, examples | Reusable templates and sample module contracts, not a host profile. |

Follow a request through API → store/admission → River → run engine → provider or module bridge. Follow a write through prepare → consent → dispatch intent → service receipt/status. Keep those boundaries explicit.

## Development setup

Use the Go version in [go.mod](go.mod). The supported runtime is Linux/amd64. Windows can run ordinary Go tests; Linux process isolation and media CLIs require Linux/Docker checks.

```sh
go mod verify
go test ./...
```

For the application, follow [local setup](docs/running.md). For SQL tests, set CLX_TEST_DATABASE_DSN to a disposable database reachable from the test process:

```sh
go test -parallel=4 -tags=integration ./...
```

Tests use isolated schemas, but the database must still be dedicated to tests. Missing DSN can skip SQL tests. A skipped test is not acceptance. For Linux race and file-worker checks, use [QA](docs/qa.md) and the [CI workflow](.github/workflows/ci.yml).

Install the reviewed developer tools with scripts/install-tools.sh. Run `make lint` with bin on PATH, or `bin/golangci-lint run --build-tags=integration ./...`. Use explicit source directories when formatting; do not rewrite archived third-party notice files. See [linting](docs/linting.md).

If a change affects generated SQL code, edit its SQL/configuration source and regenerate with the sqlc version recorded in the generated-code header and the configuration in [sqlc.yaml](sqlc.yaml). Do not hand-edit generated output. Review the generated diff.

## Change rules

Keep the change small and typed. Add behavior at the boundary that owns it. Do not add a framework, queue or persistence layer for a hypothetical requirement.

Preserve these invariants:

- Authorization, ownership and disclosure checks still apply after restart and before an effect.
- One nonterminal run owns a chat. Input acceptance and its queue work commit together.
- An idempotency key identifies one immutable request. Retries do not become new operations.
- Consent binds exact prepared parameters and current input. A new prepare needs new consent.
- An unknown write is reconciled before any repeat. Cancel is not rollback.
- Settlement occurs once. Unknown provider liability is not silently discarded.
- Complete files and credentials do not enter JS. File expiry is independent of active runs.

Add migrations for schema changes. Do not edit an already deployed migration to change existing data. Startup does not perform down migrations; document compatibility and recovery before an incompatible update.

For dependencies, use normal go.mod/go.sum versions. Follow the [dependency policy](docs/dependencies.md). Retain required notices and worker corresponding-source material.

## Documentation is part of the change

**Every change must include the necessary documentation additions or corrections.** Update documentation in the same PR when you change behavior, a contract, configuration, a tool, a limit, a failure mode or an operator procedure. For an internal-only change, check the affected documentation and state in the PR why no text change is needed. This is a required review check, not an optional follow-up.

Write technical documentation. State prerequisites, exact inputs, outputs, limits, errors and recovery. Prefer short sentences, active voice, consistent terms and explicit subjects. Use Simplified Technical English (STE) where practical. Preserve exact identifiers and established technical terms. Depart from STE when it would make the explanation harder to understand or reduce readability; use the clearest precise wording. Do not force a mechanical translation or a restricted word list onto code examples or domain terms.

Keep instructions separate from run reports. Document supported behavior and how to verify it. Put private receipts, logs, screenshots and acceptance reports in CI artifacts or ignored local storage. A guide must not depend on a developer's private path, test port, account or server. Use example.com, explicit placeholders and reusable commands. Do not publish filled configuration or secrets.

Use one authoritative document per contract. Link to it instead of repeating defaults and endpoint details. Keep README as the entry point. Keep each page focused on one task or boundary. Split a long guide into named topics and provide a short index; do not make readers scan a full API or QA manual for one operation. Update built-in module/tool help and schemas with the implementation; models must be able to find the same usable method documentation as developers.

Before review, check that links resolve, commands/flags exist, examples match their schemas, and implementation status is accurate. Distinguish implemented behavior, fixture coverage, live-provider checks and installation acceptance. Do not convert a historical PASS into a promise about every deployment.

## Tests and pull requests

Prefer behavioral happy and critical paths. Test the changed observable guarantee, not the current internal layout. Use fixtures for ordinary checks and real dependencies for the boundary they are meant to prove. Keep paid calls and destructive fault injection out of routine tests.

Before opening a PR, run an independent review loop and fix valid findings. Then wait for CI and resolve bot/reviewer findings. Include the problem, resulting behavior, relevant validation and remaining limits. Do not add a passing test solely to prove a removed feature is absent.

A PR may change reusable templates, but must not change another operator's deployment or secrets. Publishing, deployment and merge follow the repository's authorization process.
