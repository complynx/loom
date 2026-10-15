# Temporary-file acceptance — 2026-10-12

Result: **8 container/API scenario groups PASS**, plus populated-upgrade, active-run expiry/quota, open-stream expiry and compaction privacy behavior tests. Baseline file processing has a separate [21-group acceptance report](../2026-10-12-file-api-acceptance/README.md).

The isolated stand used synthetic Zitadel/model/transcription peers, real PostgreSQL/pgvector, a real registered acceptance service, the actual API and sandbox, and the existing restricted file worker. No real OpenAI calls, production changes, machine CA changes, or protected live-provider configuration access. Image identities are recorded separately. API was built from the working tree over f1c30a4; source changes are committed with this report.

## Proven behavior

1. `modules.artifacts.fetch({module:"fixture",method:"fetch_file",external_id:"fixture-file"})` imports 51 exact bytes outside JS and returns an owned internal ID, source IDs and fixed expiry. Wrong owner/org and missing artifact permission cannot download. `modules.artifacts.read` is absent from the actual JS namespace.
2. Image resize outputs inherit the original file deadline exactly, with PostgreSQL-compatible timestamp precision.
3. An API restart preserves the original deadline.
4. Original, derived, uploaded and handle content returns 404 after expiry, before collection.
5. Refetching the same service-owned external ID yields a new internal ID with the same checksum.
6. Background collection physically removes bytes from the QA artifact volume. Run and wallet reservations remain zero.
7. An unknown external ID and a write-method target fail through the normal `tool_failed` envelope; neither adds artifact metadata nor holds credits.
8. Restoring deployment `ttl_seconds: 86400` yields an exact 24-hour deadline on a new upload.

Short-lifetime groups use a **10-second test TTL**, not a claim that wall-clock 24-hour waiting was performed. The final default check verifies the actual configured 86400-second lifetime. Physical collection uses separate minute sweeps and can lag healthy expiry by about two minutes. Access closes at expiry independently.

## Automated and independent verification

- Full Windows integration suite with isolated PostgreSQL: PASS (`go test -parallel=4 -tags=integration ./...`). The focused file/lifecycle tests also pass after the timestamp correction.
- Full Linux race/integration suite with real pinned Poppler/FFmpeg and PostgreSQL: PASS (`go test -race -parallel=4 -tags=integration ./...`).
- Golden golangci-lint: 0 issues. `git diff --check`: PASS.
- Populated legacy upgrade test: old blobs receive `created_at + 24 hours`; legacy inline image and nested base64 copies are removed; text, opaque compaction context, usage and reservations remain intact.
- Active queued run does not protect expired files; physical collection releases quota and is safe to repeat. An already open download stops reading at its file deadline. Maximum configurable TTL succeeds against PostgreSQL.
- Review loop: 2 fresh independent textual reviews. First found legacy checkpoint copies; migration cleanup and behavioral proof added. Second: no actionable findings. Staged diff remained empty throughout the loop.

## Corrections and boundaries

Acceptance caught a real timestamp issue: separate nanosecond creation/deadline instants differed from PostgreSQL microsecond precision and could exceed the seven-day ceiling at its boundary. One truncated creation instant now sets both values; exact derived expiry and maximum TTL are verified.

Fixture corrections were kept separate from product changes: wrong image filename, a 75-second GC expectation shorter than the two-phase maintenance schedule, and an `error_code` assertion where the normal script envelope returns `error:tool_failed`. Those attempts were not counted as PASS.

This does not prove target-server acceptance, a real source-service adapter, real-model file understanding or paid provider transcription. Source services must implement the read contract and own immutable/versioned external IDs. Refreshing an expired initial attachment inside the same paused run remains unimplemented; refetch and start a fresh run. Physical deletion may be delayed by backlog, storage/database outages, or recovery mode. Backup operators must exclude temporary bytes and retire any historical byte snapshots; Loom cannot delete third-party backups or provider-side copies. Legacy checkpoint scrubbing is irreversible; schema rollback does not restore bytes.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
