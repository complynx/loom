# History, sessions and events

[API index](../api.md)

## History and search

History routes require `chat.read` and access to the chat. For simple pagination,
use `GET /v1/chats/{id}/history?after=0&limit=50`. The response is `{events:[...]}`;
use the last event's `seq` as the next `after`. The limit is 1–100. These chat
sequence numbers are separate from run SSE sequence numbers.

For bounded excerpts, send `POST /v1/chats/{id}/history/read` with
`{after_seq,before_seq,limit,max_bytes,snippet_chars}`. To read one event's text,
send `{event_seq,offset_chars,max_bytes,snippet_chars}`. Continue partial text with
`next_text_offset` until `text_partial` is false. Character offsets count Unicode
characters, not UTF-8 bytes. Original events remain stored.

Search uses `POST /v1/chats/{id}/history/search`:

```json
{"mode":"literal","query":"appointment","after_seq":0,"limit":20,"max_bytes":65536}
```

`mode` is `literal` (default), `fulltext`, `regex` or `semantic`. Text modes require
a nonempty `query`. Filters include `before_seq`, `after_seq`, `source_service_id`
and `event_types`. `case_sensitive` controls literal and regex matching. Fulltext
uses PostgreSQL `simple`; regex uses Go RE2 without lookaround or backreferences.
An invalid pattern returns `422 invalid_pattern`. Query, scan and execution limits
are configured by the deployment.

Search/read responses include `events`, `partial` and, when applicable, `reason`,
`next_cursor`, `truncated_seqs` and `skipped_seqs`. Default excerpts allow 64 KiB
and 500 characters per event. For text event pagination pass `next_cursor` as
`after_seq`, retaining the same query and filters. An event too large to scan is
listed in `skipped_seqs`, and the cursor advances. A skipped or partial result does
not prove there was no match. Read an event separately for its complete text.

Semantic search requires `search.semantic_enabled: true` and pgvector in Loom's
database. Send `mode: "semantic"`, `embedding_model`, `embedding`, `offset`, `limit`
and filters. Use `next_offset` for continuation; vectors must match the indexed
model and dimensions. Index with `POST /v1/chats/{id}/history/embeddings` and
`{seq,model,vector}`; this also requires `chat.embeddings.write`. Loom uses exact
cosine ranking with authorization filters; it provides neither an embedding provider
nor an ANN index. When disabled, the indexing route and semantic tool are absent,
and semantic search is denied.

## Sessions and versions

`POST /v1/auth/sessions` accepts `{}` under verified user credentials or a service
plus fresh subject credentials. It returns `201` with `auth_session_id`,
`expires_at` and `version`. Save these values. A local session does not refresh
expired OAuth tokens.

To replace its credentials, send `POST /v1/auth/sessions/{id}/reauthorize` with
`{"version":CURRENT_VERSION}` and fresh authorization for the same subject, actor
and organization. A delegated service supplies its service Bearer token and fresh
`X-Subject-Token`, not `X-Auth-Session-ID`, for this request. Success returns `200`
with the new expiry/version and schedules retained `waiting_auth` work. Do not
create a new run. `DELETE /v1/auth/sessions/{id}` revokes the local session and
returns `204`. These routes require `chat.read`.

Settings, policy and preference updates carry the current `version` in their body.
Read before writing; stale versions conflict. Settings use the full typed object,
not JSON Merge Patch. `If-Match` does not replace body `version`.

Billing adjustment accepts `{amount_microcredits,reason}` plus `Idempotency-Key`.
It records an audited adjustment; there is no arbitrary balance-write endpoint.

## SSE

Connect to `GET /v1/runs/{id}/events` with `chat.read`. Durable run events have
monotonic `seq` values; each SSE `id` is that sequence number. On reconnect use
`Last-Event-ID` or query `after`, never both. Persist the cursor and deduplicate by
sequence number: delivery is at least once. Ignore unknown event types safely.

Common types include `run.status`, `input.applied`, `question.created`,
`approval.created`, `interaction.resolved`, `artifact.ready`, `message.completed`,
`usage.updated` and `run.resumable`. Recovery/retry can emit additional types.
The stream contains saved run events, not provider token deltas. It reads bounded
database batches; slow writes have a deadline. Reconnect to recover saved events.

With `disconnect_policy=continue`, losing all subscribers does not cancel work.
With `pause`, disconnecting the last stream requests a pause after the configured
grace period; reconnect participates in the final pause check. A one-time async
HTTP creation request is not a stream disconnect.
