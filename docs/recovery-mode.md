# Restore recovery mode

Operator-controlled restore quarantine. Acceptance evidence is recorded separately.

Enable execution.recovery_mode before starting an API against a restored database. Default is false. It is deployment configuration; no HTTP endpoint disables it. Exit requires an operator-controlled configuration change and restart after external effects are reviewed.

In recovery mode:

- Require existing restored application settings and all referenced session key IDs. Do not bootstrap an empty database.
- Initialize identity and trusted discovery, but suppress persistent caller-binding refresh writes. Classify services from an in-memory set of trusted bindings observed during this process lifetime or restored known identities; never downgrade a newly discovered service to a human.
- Do not construct providers, the run engine, artifacts mutation service or River workers. Do not start lifecycle expiry, queue recovery or artifact reconciliation.
- Keep private metrics, public process health, OpenAPI and the operator login/panel available. Normal readiness returns 503 with recovery_mode.
- After normal authentication/permission checks, allow only diagnostics GET/HEAD, admin settings GET/HEAD, evidenced recovery preparation, provider step reconciliation and owner-scoped cancel. Explicitly block every other registered API endpoint with 503 recovery_mode, including GET default chat and SSE. No API changes may enqueue inference or invoke external write modules.
- Diagnostics runtime exposes recovery_mode; a private clx_recovery_mode gauge and startup log identify quarantine.
- Preserve restored steps, reservations and histories. Preparation may clear an expired lease and advance its fence after explicit attestation. Do not assume an unresolved step or write had no external effect. Manual provider reconciliation keeps its existing evidence, tariff, scope and lease checks; it never dispatches inference. Cancellation resolves an existing owner without account bootstrap. It retains unresolved reservations under existing rules. For a Running run it only records cancel_requested; with workers stopped, cancellation does not immediately terminalize the run or make receipts reconcilable.

Before starting or restarting recovery, keep the complete service identity inventory since the backup in identity.service_subjects, including removed services. Newly observed identities in recovery are remembered only in process memory; that set cannot survive a restart. Do not assume the restored service_identities table includes post-snapshot actors.

Running cancel remains request-only until preparation. Operators can now use `POST /v1/admin/runs/{id}/recovery/prepare` with `operations.reconcile`, an Idempotency-Key, and this body:

```json
{
  "expected_fence": 1,
  "evidence": {
    "kind": "operator_attestation",
    "reference": "incident-or-restore-record",
    "reason": "All writers stopped; inspect provider receipts and downstream effects"
  }
}
```

Read the actual fence from `GET /v1/admin/diagnostics/runs/{id}` using `diagnostics.read`; do not guess it. Preparation is allowed only while this API runs in recovery mode. It checks the principal organization, the observed fence, Running state, database-clock lease expiry and unresolved steps, writes or platform exposure. It atomically sets reconciling/recovery_prepared, clears the expired lease, advances the fence, records an audit event and commits the idempotency receipt. It does not enqueue, dispatch, settle, declare a write successful, or discard reservations. Repeating the same command/key does not advance the fence again; changed evidence under the same key conflicts. Live lease and stale fence return 409. Ordinary mode returns 409 recovery_mode_required.

After preparation, use the existing owner cancel and evidenced provider reconciliation endpoints. Unknown write/tool/platform exposure needs its corresponding external evidence and review; preparation does not add a settlement API for those records. A lost post-snapshot record is still not recreated. Preparation attestation describes stopping all writers; the API cannot verify that another replica or external call is absent.

Before disabling recovery, stop every other writer/replica, inspect all restored nonterminal runs and external effects since the restore point, settle evidenced provider receipts, and cancel or otherwise freeze unsafe pending work. Disabling the flag does not automatically determine whether old queued work is safe: ordinary mode can process retained River jobs. The operator must not release quarantine until that review is complete. Unknown writes are never resolved by inference retry.

Local acceptance will restore PostgreSQL, blob bytes and session keys from one explicit snapshot; then verify no provider calls or module write repeats while recovery is enabled, rejection of mutating/default-chat/SSE routes, authenticated diagnostics, manual receipt settlement/cancellation, clean restart/shutdown, and successful deliberate operation after review and recovery exit. A model service effect performed after the snapshot remains external evidence; missing post-snapshot records are not silently reconstructed.

This initial mode is a dispatch barrier with controlled repair, not full PITR automation or a distributed freeze. Backups, WAL policy, RPO/RTO, key retention, effect inventory and target-server rehearsal remain deployment work. Shared PostgreSQL alone does not prevent another ordinary-mode replica from writing; all writers must be stopped before restore.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk

Local bounded acceptance: [original restore report](validation/2026-10-12-restore-recovery-qa/README.md) and [shipped preparation API rehearsal](validation/2026-10-12-recovery-prepare-qa/README.md).
