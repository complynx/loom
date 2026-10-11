# Golden lint validation

Date: 2026-10-11. Target: Go 1.27.2, Linux/amd64 Docker. Local checks only.

## Configuration and scope

The configuration uses Golden commit `b57a98e8ac187b8c401660d2205caa6523bd86fb`:
79 upstream linters plus the existing contextcheck, and goimports/golines.
The MIT notice, project import prefix and OpenPGP depguard ban remain.
See [linting instructions](../../linting.md) for pinned sources and comparison
with the Nebius Go SDK configuration.

The initial audit reported 1085 issues. Production source and behavioral tests
were cleaned across the repository. Large functions were separated into phases;
SQL ordering, transaction boundaries, API payloads, authorization, fencing,
credit settlement and cancellation behavior were preserved.

CI now verifies the config and lints integration-tagged source. Schema-isolated
PostgreSQL tests run in parallel with a limit of four per test binary.
Formatters operate only on cmd, internal and integration; dependency-license
archive source bytes were not modified.

## Results

- Configuration verification: PASS.
- `golangci-lint run --build-tags=integration ./...`: zero issues; see lint.txt.
- Configured formatter diff: empty; see format.txt.
- Full Linux `go test -race -parallel=4 -tags=integration ./...`: PASS against
  isolated real PostgreSQL 17; see linux-race.txt.
- Provider and production runtime tests repeated three times under Linux/race:
  PASS; see focused-race.txt.
- Linux/amd64 production Docker build: PASS; see docker-build.txt.
- CI actionlint: PASS (ShellCheck/Pyflakes were unavailable and disabled in this
  local actionlint invocation).
- `go mod verify`: PASS. All 171 full dependency commit pins verified by the lock checker. Dependencies and their lock were not changed.
- Linux-targeted `govulncheck -tags=integration ./...`: no vulnerabilities found; see vulnerabilities.txt.
- Diff whitespace check: PASS. Index remains empty; no staging or commit.

The first full race run failed on a test cleanup assertion: the sandbox
supervisor had already closed its listener. Cleanup now accepts net.ErrClosed
and still verifies supervisor completion. Initial evidence is retained in
linux-race-initial.txt; the final run passed.

## Review loop

Independent fresh reviews covered configuration/domain/history, sandbox and
telemetry, identity/artifacts, transaction/checkpoint paths, all store changes,
HTTP/runtime, modules/integration, and provider/run engine. The pending-call
extraction was also reviewed as a proposal before application. HTTP/runtime
and provider/engine each needed two passes. Final passes have no actionable
findings.

Two substantive findings were fixed: operation logging now observes the latest
state after dispatch/reconciliation, and the Codex JSON output schema retains
quoted keys. The subprocess fixture now reads and validates the actual schema
file. A promoted-field literal compile claim was a false positive for Go 1.27;
[Go 1.27 release notes](https://go.dev/doc/go1.27) and an uncached compiler test
confirmed support.

## Scoped exceptions and limits

No linter was disabled to clear findings. Existing Golden test exclusions remain.
Local nolint reasons cover subprocess test entry points, private test fixtures,
public error type aliases, intentionally ignored malformed Docker labels, and
structured model-visible argument errors. Every exception names its linter and
reason.

This validation does not include paid OpenAI calls, a live Codex inference,
Docker Hub publication or production deployment. Development-provider tests use
QA mailbox responses or a fake Codex executable; billing still uses API tariffs.
Zitadel has a separate live local QA report.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
