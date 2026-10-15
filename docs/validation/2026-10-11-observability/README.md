# Observability validation — 2026-10-11

Scope: application metrics and structured logs, private scrape wiring for the
existing complynx.net Alloy/Prometheus/Loki pipeline. This is local developer
validation. Production deployment, Cloud delivery and notification acceptance
remain pending.

## Results

- Linux `go test -race -tags=integration ./...`: PASS with disposable PostgreSQL 17.
  See [full output](linux-race.txt). Real issuer/model/Docker discovery calls are
  replaced by test services; this does not validate production credentials.
- golangci-lint 2.14.0: 0 issues. See [output](lint.txt).
- Prometheus parser accepts the exporter's actual metrics response (exit 0,
  no diagnostics). No separate output artifact was retained for this historical check.
- `promtool check rules`: 8 rules accepted. See [output](prometheus-rules.txt).
- `promtool test rules`: dependency down/recovery and unknown inference/settlement
  fixtures PASS. See [output](prometheus-alert-tests.txt).
- Alloy `validate` accepts the full edited server configuration (exit 0, no
  diagnostics). No separate output artifact was retained for this historical check.
- Both Compose configurations validate with placeholder environment values.
  See [output](compose-validate.txt).
- `git diff --check`: PASS in both repositories. Their staged diffs remain empty.

Behavioral coverage includes SSE flush preservation, bounded unknown-route metrics,
slow scrape isolation, JWT signature and nested secret redaction, disabled payload
logging at INFO, unknown credit reservation followed by receipt settlement, queue
age after a user answer, sandbox IPC health without spawning a worker, and runtime
metrics collection during an actual agent run against test services.

Two fresh independent diff-review passes were completed. The first identified
queue age measured from run creation instead of its latest queue transition. This
was corrected and covered by a behavior test. The second found no actionable
issues. Reviewers ran no tests or mutations; the execution evidence above was
collected separately.

## Tool images

Test execution uses Go 1.27.2:
`golang:1.27.2-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61`.

Prometheus validation:
`prom/prometheus@sha256:efd719c99d83b060d9daefdcf00360461adf279f45ef5391f8d111892118753e`.

Alloy validation:
`grafana/alloy@sha256:2aa2099af76c0098d4af7a4d6e48f86cb66dc1a000222ad927a1c67c6542d13f`.

Validation adds no Go dependencies. These validation images do not change the
server's configured image policy.

## Deployment boundary

The server configuration patch (retained in operator-local files) records the local scrape target,
monitoring network attachment and `sys.env` compatibility change. No production
network, container, alert, notification route or Grafana dashboard was created.
Creating the external monitoring network and applying these changes are deployment
steps. No commit or push was performed for this stage.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk