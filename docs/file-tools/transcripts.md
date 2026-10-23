# Durable transcripts

[Transcription limits](transcription-limits.md)

## Reuse and complete text

`audio_transcribe` accepts exactly one `handle_id` or `artifact_id`. Use the
returned `source_artifact_id` for a later run, including after binary expiry.
Handles stay bound to their original run. The call first checks retained text,
before source refresh, WAV extraction, provider dispatch or paid admission.
Set `reuse_only: true` when only existing text is authorized. A miss returns
`{"error":"transcript_not_found"}` without processing or billing. This flag
does not change the processing identity.

The result includes `transcript_id`, `source_artifact_id`, bounded `text`,
`segments`, actual `start`/`end`, the source's `next_cursor`, original `provider`,
`model`, `provider_request_id` and `reused`. The preview is at most 32 KiB.
`partial` can indicate truncated text or timestamps. New results use durable IDs
instead of temporary transcript artifact/handle IDs.

Read the complete UTF-8 text through the existing script tool:

```javascript
return await modules.files.transcript_read({
  transcript_id: "TRANSCRIPT_ID",
  offset: 0,
  limit: 8192
});
```

The response contains `text`, `offset`, `next_offset`, `partial` and
`transcript_id`. `offset` and `limit` are integer byte counts; the default limit
is 8192 and the maximum is 32768. Copy `next_offset` as `offset` until `partial`
is false. Offsets must be UTF-8 character boundaries. This pagination is separate
from `audio_transcribe.next_cursor`, which selects the next source-audio chunk.

Text and provider metadata are stored in PostgreSQL, independently of temporary
source/WAV bytes. Normal binary expiry, collection and process restart do not
remove them. Retained text is not a permanent binary artifact and does not keep
the source or WAV alive. Provider receipts and full stored results remain bounded.

## Identity, permissions and billing

The identity includes the owner, organization, canonical source artifact/version,
requested language, prompt and requested start/end. Omitted start/end mean zero.
A source refresh for that exact version retains its identity. A different upload
or different explicit processing parameters is a different operation. Provider
and model settings are provenance, not cache keys: changing either does not
silently regenerate completed text.

Every reuse and page read checks current owner/org, artifact/chat permissions,
source-service authority where applicable, and current/run disclosure policy.
Expired bytes are not fetched to perform these checks. Source chat deletion and
revoked rights deny access. Another owner cannot read text, processing state or
provenance. Reuse is not an authorization grant.

Concurrent admission binds one original paid step to the processing identity in
the same transaction as reservation. A dispatch marker is irreversible. Pending
or unknown work pauses subsequent requests with `transcription_in_progress`;
neither restart nor a new call ID resends it. Operator reconciliation retains
the original step, receipt and reservation. A definite rejection settled with
zero usage releases the unused identity for a later request; the rejected step
and ledger remain in the audit history. Unknown outcomes never release it. A saved result becomes reusable only
after that original step is settled. Reuse creates an ordinary durable result
checkpoint without a credit reservation, settlement or provider usage.

Only an explicitly requested new processing operation may supply a new UUID
`reprocess_key`. Keep that key unchanged for retries of that operation. Its result
does not replace the default original transcript. A pending/unknown operation
still blocks another paid admission, including one with a different reprocess key.

## Upgrade and rollback

Migration `00006_audio_transcripts.sql` adds the text/provenance table without
changing artifact deadlines or existing paid receipts. Drain old ASR executors
before admitting work with the new binary. Do not overlap old and new ASR writers:
old executors do not understand the new cross-run admission identity.

On first reuse, a pre-upgrade paid receipt is associated with its source and
parameters. If its full temporary text artifact is still readable, Loom copies
that complete text into durable storage without ASR or billing. A bounded old
preview is never promoted to complete text. If full text was already collected,
the call pauses with `transcript_result_unavailable`; unknown old work remains
blocked. Missing historical text cannot be reconstructed without explicitly
requested reprocessing after the original outcome is resolved.

Consumers must accept `transcript_id` and use `files.transcript_read`; historical
tool receipts retain their old temporary handle fields. Before binary rollback,
stop new ASR admission and resolve pending work. Keep the additive table and
completed records. Older binaries cannot serve the durable transcript contract
or safely deduplicate new ASR work. Do not run the down migration to roll back a
binary: it deletes retained text and requires a separate authorized data-removal
and backup plan.
