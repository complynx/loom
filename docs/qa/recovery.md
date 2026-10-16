# Failure and integration QA

Run destructive scenarios only on owned disposable resources. Use the [failure contract](../failures.md) and [restore barrier](../recovery-mode.md).

## Repeatable fixture checks

Prepare the disposable database in [setup](setup.md), then run from the repository root:

```sh
go test -v -race -parallel=4 -tags=integration -count=1 ./integration \
  -run 'Test(UnknownMutationRecoversWithStatusAndNeverRepeatsEffect|GracefulStopPersistsUnknownInferenceWithoutRepeatingIt|HybridRetryDurableScheduleRestartAndSingleUsage)$'
go test -v -race -count=1 ./internal/store \
  -run 'Test(ExhaustedQueueRecoveryPreservesUncertaintyAndLiveWork|RecoveryPreparationPreservesUncertaintyAndSettlesOnce|RecoveryPreparationKeepsUnknownExternalWrite)$'
go test -v -race -count=1 ./internal/lifecycle -run '^TestShutdown'
```

These tests use controlled failures and fake external peers. For the production
River/startup path, run `scripts/linux-tests.sh` with the test DSN; it includes
[`TestRuntimeRunsQueuedRequestAndShutsDown`](../../internal/app/runtime_test.go).
None of these commands performs a database snapshot restore or a host disk-full test.

Before each stand fault, record the run/operation IDs, ledger state and service
receipt count. Specify the injection point and a bounded observer timeout. Inject
one fault, restore the failed dependency, then compare durable state and external
receipts before resuming. Repeat with a fresh run for a different injection point.

## Fault matrix

| Fault | Expected result |
| --- | --- |
| SIGINT/SIGTERM | Drain/checkpoint within configured `execution.shutdown_deadline_ms` (at most 5000 ms); the example target is 1000 ms. Measure process exit separately from Docker event latency and report forced termination or incomplete cleanup. |
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
