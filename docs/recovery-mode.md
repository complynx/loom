# Restore recovery mode

Operator-controlled restore quarantine. Acceptance evidence is recorded separately.

Enable execution.recovery_mode before starting an API against a restored database. Default is false. It is deployment configuration; no HTTP endpoint disables it. Exit requires an operator-controlled configuration change and restart after external effects are reviewed.

In recovery mode:

- Require existing restored application settings and all referenced session key IDs. Do not bootstrap an empty database.
- Initialize identity and trusted discovery, but suppress persistent caller-binding refresh writes. Classify services from an in-memory set of trusted bindings observed during this process lifetime or restored known identities; never downgrade a newly discovered service to a human.
- Do not construct providers, the run engine, artifacts mutation service or River workers. Do not start lifecycle expiry, queue recovery or artifact reconciliation.
- Keep private metrics, public process health, OpenAPI and the operator login/panel available. Normal readiness returns 503 with recovery_mode.
- After normal authentication/permission checks, allow only diagnostics GET/HEAD, admin settings GET/HEAD, evidenced provider step reconciliation and owner-scoped cancel. Explicitly block every other registered API endpoint with 503 recovery_mode, including GET default chat and SSE. No API changes may enqueue inference or invoke external write modules.
- Diagnostics runtime exposes recovery_mode; a private clx_recovery_mode gauge and startup log identify quarantine.
- Preserve restored steps, reservations, histories and leases. Do not assume an unresolved step or write had no external effect. Manual provider reconciliation keeps its existing evidence, tariff, scope and lease checks; it never dispatches inference. Cancellation resolves an existing owner without account bootstrap. It retains unresolved reservations under existing rules. For a Running run it only records cancel_requested; with workers stopped, cancellation does not immediately terminalize the run or make receipts reconcilable.

Before starting or restarting recovery, keep the complete service identity inventory since the backup in identity.service_subjects, including removed services. Newly observed identities in recovery are remembered only in process memory; that set cannot survive a restart. Do not assume the restored service_identities table includes post-snapshot actors.

Running cancel remains request-only. There is no shipped recovery preparation API/CLI to move an expired uncertain Running run into a reconciliation-eligible state without workers. The local rehearsal used a QA developer one-shot calling the existing ClaimRun uncertainty guard. Keep quarantine until an approved repair procedure covers these runs.

Before disabling recovery, stop every other writer/replica, inspect all restored nonterminal runs and external effects since the restore point, settle evidenced provider receipts, and cancel or otherwise freeze unsafe pending work. Disabling the flag does not automatically determine whether old queued work is safe: ordinary mode can process retained River jobs. The operator must not release quarantine until that review is complete. Unknown writes are never resolved by inference retry.

Local acceptance will restore PostgreSQL, blob bytes and session keys from one explicit snapshot; then verify no provider calls or module write repeats while recovery is enabled, rejection of mutating/default-chat/SSE routes, authenticated diagnostics, manual receipt settlement/cancellation, clean restart/shutdown, and successful deliberate operation after review and recovery exit. A model service effect performed after the snapshot remains external evidence; missing post-snapshot records are not silently reconstructed.

This initial mode is a dispatch barrier with controlled repair, not full PITR automation or a distributed freeze. Backups, WAL policy, RPO/RTO, key retention, effect inventory and target-server rehearsal remain deployment work. Shared PostgreSQL alone does not prevent another ordinary-mode replica from writing; all writers must be stopped before restore.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk

Local bounded acceptance: [restore report](validation/2026-10-12-restore-recovery-qa/README.md).
