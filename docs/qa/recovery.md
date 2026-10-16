# Failure and integration QA

Run destructive scenarios only on owned disposable resources. Use the [failure contract](../failures.md) and [restore barrier](../recovery-mode.md).

## Fault matrix

| Fault | Expected result |
| --- | --- |
| SIGINT/SIGTERM | Drain/checkpoint and process exit within five seconds; happy-path target near one second. Measure process exit separately from Docker event latency. |
| SIGKILL after model dispatch or service write | Retain uncertainty, ledger and operation ID. Do not blindly repeat a write. |
| DB outage/read-only/ENOSPC | Do not hide write failure behind connectivity readiness. After recovery, preserve idempotency and prior effects. |
| Artifact ENOSPC | No readable truncated artifact. Preserve quota/metadata integrity and restore healthy work. |
| Worker timeout/OOM/kill | Bounded cleanup, visible status, healthy neighbor and next script survive. Shared container limits are not per-worker isolation. |
| Queue restart/exhaustion | Sweep does not take live work. Safe exhausted runs require resume; unknown outcomes require reconciliation. |
| Restore older DB snapshot | No dispatch before effect review. Restore keys. Reconcile service effects after the backup point before resuming work. |
| Collector outage | API and shutdown stay responsive; telemetry loss is visible. |

## External client integration

Use real Zitadel, Loom, the service broker and synthetic business data on the client's own stand. Test message ingress → durable task → delegated run → result, history in the next run, and exactly one delivery. Verify writes against service receipts, not only the model's final text.

For a source-backed file, test source API → opaque external ID → Loom import → worker → authorized download. Do not attach original bytes at run creation when the test is meant to prove fetch.

Test client restart, combined IAM/Loom outage and exact approval separately. A new conversational UI needs its own acceptance even when older binary callbacks work. A manually driven provider proves the transport and runtime, not unattended model quality.

## Installation telemetry

Verify private scrape/log/trace ingestion, backend reads, rule evaluation and actual notification delivery separately. Exporter presence does not prove a usable dashboard. Keep high-cardinality identity/run IDs in logs, not metric labels.

Record the tested load envelope, queue/SSE behavior and shutdown limits. A successful bounded local test is not a worst-case host guarantee.
