# Retention

[Guide index](../data.md)

## Retention and deletion

History retention accepts a duration in days or `forever`. The policy is copied to each new chat as `retention_days`; changing settings does not rewrite existing chats. Lifecycle cleanup removes expired chat events and summaries only when the chat has no nonterminal run.

Artifacts have a separate, finite lifetime. `artifacts.ttl_seconds` defaults to 86,400 seconds (24 hours), with a maximum of seven days. The database stores `expires_at`. An active run or chat reference does not extend it. Reads reject expired artifacts before physical cleanup finishes. Legacy `artifact_retention` remains in settings for compatibility and does not control file expiry. See [file references](../file-tools/references.md) for retrieving an expired file again from its external source.

Cleanup marks expired artifacts as deleted, then the artifact collector removes their bytes and records collection. Collection is asynchronous; a tombstone does not prove that bytes have already been unlinked. Recovery, financial, credential and audit records remain stored. There is no configurable expiry or cleanup for `idempotency_records` or `passive_event_receipts`; these receipts survive history removal and prevent reuse of accepted keys. External backends have their own deduplication windows.

The optional [action journal](../observability.md#action-journal) uses local file rotation, independently of database retention. Duration-based `trace_retention` is rejected; `forever` or an unspecified value is accepted. Application logs, action journal files and exported telemetry follow the operator's separate storage and retention policies. `forever` does not remove storage quotas or prevent explicit deletion.

Deleting a chat marks it deleted, blocks access and new runs, cancels and fences active runs, and removes chat events, context summaries, cached prefixes and artifact handles in the database transaction. Artifacts attached only to deleted chats are marked for physical collection; artifacts also attached to a live chat remain until their own expiry. Deleting the default chat lets the next request create a new default chat atomically.

Chat deletion is not complete erasure of every copy of message content. Run documents, steps and recovery records remain, and can contain accepted input or provider data. Unknown external operations and uncertain credit reservations remain available for reconciliation. Personal memory has its own [clear API](../user-memory.md). Copies in provider storage, exported logs and backups also follow their respective policies.

After restoring an old backup, reconcile deletions, revocations and external effects before execution. An automatic post-restore barrier or replay of deletion tombstones is not implemented. Use [recovery mode](../recovery-mode.md) for the restore procedure. An operator must define backup retention separately; user deletion does not immediately remove historical backups.

## Context reuse and personal memory

`previous_run_id` is an optional hint that must reference a terminal run in the same chat, owner and organization. Only a compatible `completed` run can supply a cached history prefix. The runtime checks actor, model, effort, settings version, absence of input artifacts, cache expiry and source event count. Current authorization and micro memory are loaded separately. The prefix expires after 15 minutes. Provider cache savings are not guaranteed, and execution tool checkpoints from the previous run are not copied.

[Active-run compaction](../active-compaction.md) stores a new input window separately from history and durable effects. Native Responses compaction and fallback summarization use separately accounted paid steps.

[Personal memory](../user-memory.md) belongs to an issuer, subject and organization. It provides a micro profile of up to 500 Unicode code points and three levels of on-demand data. It needs no additional memory role, survives chat deletion, and is cleared through its own API.

The implementation boundaries are in [lifecycle cleanup](../../internal/store/lifecycle.go), [artifact collection](../../internal/app/maintenance.go), [context reuse](../../internal/store/run_context.go), and the [migrations](../../migrations).
