# Bounded shutdown fix, 2026-10-11

The application now reserves process-exit headroom inside `execution.shutdown_deadline_ms`. With the default 5000 ms total budget, parallel component stopping and artifact/database cleanup receive 4000 ms. Smaller configured budgets retain 80% for cleanup; headroom is capped at 1000 ms. Healthy shutdown does not wait for either budget.

`internal/lifecycle/shutdown.go` bounds both component waiting and cleanup. Forced HTTP closure runs asynchronously after the cleanup deadline, so it cannot extend application exit. A completion/deadline race reports incomplete shutdown whenever the cleanup context has expired. Runtime warnings remain explicit: the service does not claim checkpoint durability when PostgreSQL is unavailable.

## External timing and measurement correction

All measurements use Docker timestamps from the same clock. SIGTERM is the `kill` event with signal 15. **Actual process exit is container `State.FinishedAt`; the `die` event is a later Docker daemon notification.** The earlier full-runtime report used signal-to-`die` as the strict exit metric. Its raw artifacts also prove the original process itself exceeded five seconds: 5011.45 ms, followed by 843.20 ms of notification delay.

| Scenario | Signal to process exit, ms | Signal to die event, ms |
|---|---:|---:|
| Original outage before fix | 5011.45 | 5854.65 |
| Fixed healthy, idle | 7.11 | 327.28 |
| Fixed PostgreSQL paused, first | 4005.53 | 4250.12 |
| Fixed PostgreSQL paused, second | 4004.28 | 4339.28 |
| Fixed active fake inference, PostgreSQL paused | 4004.49 | 4319.14 |
| Final reviewed code, healthy active inference | 38.60 | 521.54 |
| Final reviewed code, PostgreSQL stopped | 4006.81 | **5925.31** |
| Final reviewed code, stopped PostgreSQL repeat 1 | 4006.79 | 4903.44 |
| Final reviewed code, stopped PostgreSQL repeat 2 | 4.38 | 324.24 |

Every fixed process exited 0 without OOM or SIGKILL, below 5000 ms. Both healthy processes were below the 1000 ms target, including active inference and its durable checkpoint. The **5925.31 ms die-notification result remains in [final-results.json](final-results.json) as a failed original event-based threshold**; it is not rewritten as a pass. Its actual process exited after 4006.81 ms and Docker emitted `die` 1918.50 ms later. The application cannot bound Docker daemon notification latency. [process-timings.json](process-timings.json) records both metrics; each scenario retains raw events, full logs, and container state. The original input timestamps remain in [the full-runtime report](../2026-10-11-full-runtime-qa/REPORT.md).

A new baseline binary before this patch also reproduced 5347.22 ms signal-to-die and 5000 ms internal waiting; [before-events.jsonl](before-events.jsonl) and [before-api.log](before-api.log) retain that proof. [binaries.json](binaries.json) and [final-binary.json](final-binary.json) identify the executable snapshots.

## Durability and scope

The healthy active run ended `paused`, reason `shutdown`, with its lease cleared and no remaining credit reservation. [final-db.txt](final-db.txt). The database-blackhole active run could not commit a shutdown checkpoint: after PostgreSQL was unpaused, its last durable state remained `running` with an expired lease, pending model step, and 8381 reserved microcredits. [active-after-db.txt](active-after-db.txt). This is an honest interrupted state, requiring existing recovery/reconciliation; this lane does not claim recovery acceptance or successful persistence while the database is unavailable.

The fixture used a real Linux application, real PostgreSQL/River queue, and actual sandbox supervisor. All inference endpoints were deterministic local HTTPS peers, with no paid provider call. PostgreSQL was paused only in this owned fixture, then independently stopped for the final reviewed-code checks. No retained OpenAI/Zitadel stand or shared test database was changed.

Focused Windows tests and ten Linux race repetitions passed. [focused-tests.txt](focused-tests.txt), [linux-race.txt](linux-race.txt). Behavior tests cover blocked stops, blocked final cleanup, stop-context cancellation, immediate healthy completion, and expired-budget reporting. Independent source review found the deadline/completion race, which was fixed before final reviewed-code validation; the following review was clean.

## Replay and cleanup

[compose.yaml.txt](compose.yaml.txt), [config.yaml.txt](config.yaml.txt), [peers.go.txt](peers.go.txt), and [verify-stopped.ps1.txt](verify-stopped.ps1.txt) preserve the final fixture. Copy the files into `management.local/shutdown-fix`, temporarily name the peer source `peers.go` for `go run ... init` and its Linux build, then rename it back to `.go.txt` to avoid adding a package to repository-wide gates. Build the application as `clx-final` with the repository's pinned Go Docker image, start Compose, and run the replay script. Port 58480 and subnet 10.254.246.0/24 are fixture-local and must be checked for availability before replay. Replay currently preserves the old signal-to-die threshold output; compare actual process exit via raw `FinishedAt` as above.

All six fixture containers, two named volumes, and its network were removed. Temporary TLS/session-key files were deleted. Development caches, binaries, and non-secret replay files remain ignored. [cleanup.txt](cleanup.txt).

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
