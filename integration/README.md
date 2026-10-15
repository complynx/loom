# Behavioral integration tests

These tests use a real PostgreSQL server and exercise the HTTP API, durable storage, the run engine, and the Responses HTTP adapter. Each test creates a unique PostgreSQL schema, runs the embedded migrations, and removes only that schema during cleanup. Tests do not change production data. Independent fixtures run in parallel; use `-parallel` to bound concurrent PostgreSQL connections on smaller test hosts.

Set `CLX_TEST_DATABASE_DSN` to a disposable PostgreSQL database, then run:

```sh
go test -tags=integration ./integration -count=1
```

If the variable is absent, the suite skips. A skipped suite is not evidence that the PostgreSQL paths work.

Covered client behaviors:

- Default chat stability, run creation and replay, conflicting idempotency requests.
- One active run per chat under concurrent HTTP requests, independent chats, and cross-user denial.
- Durable history, SSE replay cursors and Last-Event-ID, revocation, last-subscriber disconnect grace, reconnection, passive activity deduplication, and regular expression search.
- Revision-checked preference updates.
- Request to final agent result, durable history, credit settlement, and completed-run recovery without another inference.
- Choice question, invalid answer rejection, accepted answer, and continuation on a new worker.
- Steering, pause, and resume.
- Budget exhaustion and revoked execution authorization before provider dispatch.
- Graceful shutdown and provider cancellation; uncertainty retains reservations when sponsored recovery is disabled or excluded. Separate provider-recovery tests cover bounded platform-sponsored inference retries, release of user reserves and platform exposure accounting.
- Paid history compaction that preserves user constraints and provenance, pins the current request, and reuses the summary after a question and worker restart without a duplicate charge.
- Full mutation flow through the production module bridge: service manifest discovery, preparation, explicit HTTP approval, passive activity that cannot approve, stable operation ID, and one verified external effect.
- Mutation reply loss after an external effect, followed by explicit run resume and a status lookup with the same operation ID, without repeating execution.
- Privileged HTTP provider-receipt reconciliation: organization isolation, validated tariffs, sanitized responses, one settlement, and continuation from verified output without another inference.
- Owner-authorized chat deletion: repeated deletion is safe, deleted history and runs become inaccessible, and a replacement default chat accepts the next request.
- Deleted chats reject original run-create, control, and input idempotency keys without returning retained private content.
- Control and input receipts remain content-free after chat-read permission is revoked, including idempotent replay.
- Paid incomplete model responses preserve usage but execute no tool call and resume without orphan function-call items.
- Client cancellation during an outstanding HTTP inference remains authoritative over a late final answer; cancelled runs cannot resume.

Additional scenarios cover active-run compaction, previous-run cache compatibility, bounded search/read, disabled vector search, temporary artifact handles/direct attachment, personal micro-memory reload without separate memory roles, and persisted hybrid retry schedules. These are behavioral fixtures; they do not prove live model quality.

The model endpoint is an HTTP test server. The API authenticator and the engine authorization callback are explicit boundary fakes. Identity protocol behavior has separate tests in `internal/identity`. The suite invokes `Engine.Execute` directly; it does not prove River scheduling, process startup, Docker confinement, a live model provider, or production acceptance. Those boundaries require their own checks.
Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
