# Cancellation checkpoint repair — 2026-10-11

The proven state-read cancellation gap is repaired. The execution loop retains its last successfully loaded owned run and fence when the next database read fails. A cancelled read reloads durable state with the existing bounded uncancelled checkpoint path. A client cancellation remains authoritative over pause, shutdown, and a previously returned model answer. A concurrent unrelated database error is still returned after the stop checkpoint attempt.

## Reproduction and proof

Before the patch, the permanent regression failed in all three original cases: cancel, pause, and shutdown each returned `context canceled` from the interrupted owned state read. Simply adding a checkpoint call would have been insufficient: assigning the failed read directly to `run` also replaced the known owner and fence with zero values.

The permanent behavioral test is `integration/cancellation_checkpoint_test.go`, `TestInterruptedStateReadCheckpointsControl`. It injects cancellation at a Store state-read boundary after claim, while using real PostgreSQL and the public HTTP control API. It proves terminal cancellation and denied resume, resumable pause/shutdown, empty final answers, no inference dispatch, and released reservations. Its additional concurrent database-error case proves that the original failure remains visible while durable cancellation is recorded.

The exact diagnostic from the original failure report was also rerun with acceptance assertions replacing defect observations. This supplementary overlay blocks the execution-loop state read **after verified inference**. It proves `Cancelled`, `client_cancel`, empty result, spent 15, and reserved 0. The synthetic model returns a final answer, which cancellation prevents from becoming the run result. The original report and original diagnostic remain unchanged.

| Evidence | Result |
| --- | --- |
| windows.txt: permanent read-boundary cases plus existing late-final cancellation test, count 50 | PASS |
| linux-race.txt: same tests, count 20, Linux race detection | PASS |
| paid-boundary-windows.txt: exact original post-inference diagnostic, count 20 | PASS |
| paid-boundary-linux-race.txt: same diagnostic, count 20, Linux race detection | PASS |
| error-preservation.txt: final permanent regression including unrelated database error, count 3 | PASS |
| golden.txt: final scoped Golden gate for integration and runs | PASS, 0 issues |

The first four runs preceded only the unrelated-error classification correction. The final focused run covers that correction. Global final gates are reported by the root acceptance lane.

## Reproduction

Set `CLX_TEST_DATABASE_DSN` to the disposable local PostgreSQL fixture, then run:

```text
go test -tags=integration ./integration -run '^Test(InterruptedStateReadCheckpointsControl|CancelDuringInferenceCannotBeOverwrittenByLateFinal)$' -count=1
go test -overlay=docs/validation/2026-10-11-cancel-fix/overlay-windows.json ./integration -run '^TestQACancelAtDatabaseReadDiagnostic$' -count=1
```

In PowerShell, quote the full `-overlay=...json` argument. The retained Windows overlay uses the current checkout's absolute path; adjust it for another checkout. The Linux overlay assumes the checkout is mounted at `/src`. Linux proof used the pinned Go 1.27.2 Alpine image from the root validation runbook. Each case uses an isolated schema and removes it through fixture cleanup. No paid model call, production service, credential, deployment, or repository index change occurred.

## Scope

This repair closes the proved owned-state-read cancellation window. It does not claim that all possible cancellation/database outage combinations now meet the process-wide shutdown deadline. PostgreSQL outage and sandbox transport repairs have separate acceptance reports. General non-cancellation state-read errors remain errors. Fencing still prevents a stale worker from writing another worker's checkpoint.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
