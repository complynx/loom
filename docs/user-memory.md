# Personal user memory

Personal memory belongs to the authenticated Zitadel issuer, subject, and organization. All chats of that user in that organization share it. HTTP callers cannot select another owner. Cross-user and cross-organization paths are not exposed.

## Layout

Micro memory is a separate short profile: at most 500 Unicode characters. The agent receives this text in every inference request including after compaction. Keep durable preferences and essential facts here. Empty text disables its content without deleting other memory.

The on-demand memory has three levels:

1. Index: up to 8,000 Unicode characters that point to useful notes.
2. Registry notes: entries with `kind: "note"`, a stable key, title, summary, and body.
3. Sources: entries with `kind: "source"` and an optional `parent_key` that references a note. Store supporting detail or evidence here.

The index and entries are loaded through tools. They are not all inserted into the prompt. Source references are soft links: deleting a note does not delete its sources. A parent key does not require an existing note.

Keys match `[a-z0-9][a-z0-9_-]{0,79}`. Each entry allows a 200-character title, a 1,000-character summary, and a 64,000-character body. There are at most 1,000 entries per user and organization. Limits count Unicode code points, rather than UTF-8 bytes. NUL and invalid UTF-8 text are rejected.

## HTTP contract

Personal memory is included in the consolidated `00001_loom.sql` baseline. Apply it through the normal migrator to a new database; do not run the pre-release baseline over legacy QA schemas. Personal memory is available to every authenticated user without additional roles or permission mapping. Administrators also access only their own memory.

All endpoints require a current access token. Responses use `Cache-Control: no-store`. The service checks authentication on every request. No API accepts a user ID or organization ID in its body.

| Method and path | Body or query | Result |
| --- | --- | --- |
| `GET /v1/users/me/memory` | None | `micro`, `index`, account `revision` |
| `PUT /v1/users/me/memory/micro` | `{ "text": "...", "version": 0 }` | Updated text and version |
| `PUT /v1/users/me/memory/index` | `{ "text": "...", "version": 0 }` | Updated text and version |
| `POST /v1/users/me/memory/search` | `{ "query": "...", "mode": "literal", "kind": "note", "after_key": "...", "limit": 20 }` | Entry metadata, cursor, partial flag |
| `GET /v1/users/me/memory/entries/{key}` | `offset=0&limit=4000` | Metadata, bounded body, next offset, total characters |
| `PUT /v1/users/me/memory/entries/{key}` | `{ "kind": "note", "title": "...", "summary": "...", "body": "...", "version": 0 }` | Updated entry |
| `DELETE /v1/users/me/memory/entries/{key}` | `{ "version": 3 }` | 204 |
| `DELETE /v1/users/me/memory` | `{ "revision": 5 }` | 204; clears micro, index, and all entries |

Use the version returned by the preceding read for updates and deletion. Version zero creates a new entry or sets an untouched document. A stale version returns 409 `memory_version_conflict`. Read the current value and resolve the conflict before retrying. Entry versions do not repeat after delete and recreation. Clearing all memory checks the account revision, so a concurrent change prevents a stale clear. A clear keeps a version tombstone rather than resetting versions.

An empty search query lists entries. Literal search is case-insensitive. Regex search uses Go regular expressions and `mode: "regex"`; invalid patterns return 422 `invalid_memory_regex`. Search checks keys, titles, summaries, and bodies but returns metadata without bodies. `kind` is optional. The default result limit is 20; the maximum is 100. Each call scans at most 100 entries. Continue with `next_after_key` while `partial` is true, even when the current page has no matches.

Read offsets count Unicode characters. The default read limit is 4,000; the maximum is 16,000. Continue at `next_offset` when `partial` is true. Missing entries return 404. Oversized fields or invalid keys return 422 `invalid_memory`.

## Agent tools

The script sandbox exposes `modules.memory` for every authenticated run user:

- `state({})` reads micro memory, the index, and the account revision.
- `set_micro({text, version})` and `set_index({text, version})` update the documents.
- `list({query, mode, kind, after_key, limit})` lists or searches entry metadata.
- `read({key, offset, limit})` reads a bounded entry body.
- `put({key, kind, parent_key, title, summary, body, version})` creates or updates an entry.
- `delete({key, version})` deletes an entry.
- `clear({revision})` clears all personal memory.

Each callback checks fresh authorization and the run lease before it acts. Mutation retries are conflict-safe, but do not replay the original success response: a repeated write may return a version conflict. Read the current state before deciding whether to repeat it.

## Model use and safety

Memory is user-provided data. It cannot grant rights, change system instructions, approve service writes, or override the current user request. The model receives a reminder of this boundary with the micro profile. All personal memory tools are available to the authenticated run user. Each tool call rechecks authorization and execution ownership.

Users and authorized agents curate memory explicitly. There is no automatic fact extraction, background model call, embedding call, semantic index, or automatic promotion of chat history into memory. History remains a separate source. Memory tool use costs no model tokens beyond the text sent to inference and the ordinary agent calls that request it.

Memory is retained until explicitly changed or deleted. Chat deletion and history retention do not clear personal memory. Full clear removes the stored text and entries, but cannot erase copies already present in previous provider inputs, logs, backups, or retained run context. Avoid putting credentials or access tokens in memory.

The implementation stores memory in PostgreSQL and introduces no additional dependency or provider call.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
