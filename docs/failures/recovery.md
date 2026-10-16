<a id="recovery"></a>

# Shutdown, crash recovery and backups

[Guide index](../failures.md)

## Shutdown

On SIGINT/SIGTERM, Loom closes admission/readiness, blocks new dispatch and requests durable pauses. It cancels active contexts, checkpoints known results and uncertain operations, and stops HTTP/SSE and workers within the configured shutdown budget. A `run.resumable` event is valid only when its matching state was persisted.

The example configuration sets `execution.shutdown_deadline_ms` to 5000; validation permits a smaller positive deadline, but not a larger one. Cleanup leaves exit headroom within that budget. The example `shutdown_target_ms` of 1000 is an operational target, not a guarantee. Set the container stop timeout above the application deadline, for example eight seconds for the example configuration.

Database failure, SIGKILL or blocked kernel I/O can prevent a final commit or event delivery. Recovery then relies on intent persisted before dispatch. Check the final shutdown log and rehearse database/host shutdown separately.

## Crash recovery

A new coordinator takes an expired lease, advances the fence and reads the last durable step. Interrupted model work can have uncertain usage. Sent writes require reconciliation; saved results must not execute again.

A paused run stays paused. Queued/running work can recover automatically while authorization, budget and contracts permit it; otherwise it enters the appropriate wait or reconciliation state. Recovery preserves the run and does not silently switch its model.

A script with dispatched writes is not automatically replayed as a whole. After operation reconciliation, the agent receives the interrupted source and known outcomes to form a continuation. Read-only replay still requires the applicable contract and budget. There is no general write-script replay protocol that makes new operation IDs safe.

## Backups and disaster recovery

Define RPO/RTO in the deployment's backup plan. Loom does not configure PostgreSQL WAL archiving, off-host retention or automated PITR. Back up PostgreSQL, session-key versions, source references and deletion/revocation records consistently. Database ciphertext cannot be decrypted without its referenced key versions. Long-term backups exclude temporary file bytes; durable originals belong to source services. Any short-lived snapshot of temporary bytes must preserve their original expiry.

Stop every writer before restore. Start the restored API with [recovery mode](../recovery-mode.md), inventory external operations since the restore point, reconcile usage and effects, then decide whether to release quarantine. An external operation-ID journal is needed to identify effects missing from the restored database. Without that evidence, database restore alone cannot prevent duplicate external actions.

Use a separate environment for a restore rehearsal. Record the snapshot and keys used, verify that recovery mode blocks dispatch, check that expired temporary files remain unavailable, and test deliberate operation only after the review is complete. Retrieve required source files again through their external IDs and normal authorization.

## Verification

Select the affected [QA scenarios](../qa.md), including:

- Disconnect before dispatch, after dispatch and after backend commit: preserve one action identity and report uncertainty accurately.
- Database loss during shutdown: bounded exit and no false durable status.
- Worker/coordinator kill or lease loss: no new dispatch from a stale owner.
- Duplicate input, approval and reauthorization: preserve transactional and idempotency boundaries.
- Provider usage loss: no duplicate settlement or unsupported reserve release.
- Stale discovery or revoked permissions: enforce the applicable freshness and online checks.
- SSE reconnect: replay durable events without presenting a partial preview as a final answer.
- Restore: keep keys, blobs and ledger consistent and block writes until review.
