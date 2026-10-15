# File API acceptance

Date: 2026-10-12. Source: `f1c30a40879aac1c6a8715ea0bd627f61b677fd7`, branch `codex/loom`.

**Result: 21 scenario groups PASS.** This is local functional acceptance through the real API, PostgreSQL/River, sandbox supervisor/Goja and isolated file-worker containers. Product code was not changed. No production service, real OpenAI account or live-provider configuration was accessed. Models, transcription and identity use a dedicated local HTTPS fixture.

## Evidence

- [Selected scenario receipts](results.json): actual run IDs, outcomes, cursor/size checks, image hashes, cancellation time, quota and credit assertions.
- [Final database state](final-database-state.txt): 95 completed runs and 3 cancelled runs across all attempts; run reserves and wallet reserves are zero; all four transcription steps are completed. Counts include fixture corrections and deliberately rejected operations.
- [Selected private metrics](selected-metrics.prom) and [exact local image identities](image-ids.txt).

The API image contains the reviewed production Go implementation. Changes after its build affect tests, documentation and source-distribution fetching, not runtime Go code. The worker image includes the reviewed source-mirror correction. Tests used its declared deployment restrictions: UID 10001, no network, read-only root, 512 MiB, 128 PIDs, one CPU and bounded temporary storage. Private sockets connect it to the API. The acceptance report does not imply production deployment.

## Accepted paths

1. Large UTF-8 text returns bounded pages and advancing cursors. Regex search returns matches and partial continuation. UTF-16 text decodes correctly.
2. Image info, resize and crop produce derived artifacts. Downloaded PNG dimensions and hash match metadata. Requested blue crop pixels are correct. Bytes sent directly to the model fixture equal the authorized download exactly. JS results contain handles/metadata instead of binary file contents.
3. PDF info, partial text, literal/RE2 search, page rendering and empty embedded-image listing pass with real Poppler. Info currently returns bounded native `pdfinfo` text.
4. Video metadata, initial storyboard, detailed time range, timestamp frame and WAV extraction pass with real FFmpeg. Animated GIF produces multiple sampled frames. Metadata currently exposes native FFprobe streams/format; audio presence was checked from stream types.
5. CSV, JSON Pointer, XML, HTML, DOCX, XLSX rich inline text, PPTX listing and selected-slide continuation, ODF and EPUB text/listing pass. Long selected-slide tail remains reachable. Office fixtures are synthetic minimal parts, not a fidelity test of complex desktop documents.
6. ZIP/TAR/GZIP listing and extraction stream derived data back through artifacts. Extraction uses names returned by listing; plain GZIP exposes its logical member as `content`. Traversal and excessive expansion are denied.
7. Another owner, organization and unprivileged caller cannot download the artifact. A handle cannot be used for processing in another run. Foreign handle download is denied.
8. A registered service with delegated user authorization receives byte ranges and conditional responses. Service token alone is denied.
9. Disabling file tools and transcription removes their namespace/tool declarations from the actual model request.
10. Malformed PDF, invalid regex, out-of-bounds crop and uploads above 20 MiB are denied. Valid subsequent requests still work.
11. Filling the worker's bounded 128-MiB tmpfs produces real ENOSPC. Processing fails safely; original content survives, reserves close and rendering succeeds after freeing the fixture file.
12. A separate API/database with a 2-MiB artifact tmpfs fails large uploads and derived writes on ENOSPC. Failed writes create no metadata entry; the original archive remains downloadable and a subsequent small upload succeeds.
13. A paused worker simulates an in-flight blocked file request. API cancellation terminalizes the run in 109 ms with zero reserve; processing succeeds after unpause. This measures one local fixture, not a sustained-load envelope.
14. An injected SIGABRT causes real exit 2. Docker `unless-stopped` restarts the worker automatically; restart count advances from 0 to 1 and its persistent Unix socket recovers.
15. Storage quota rejects a second upload with non-retryable `429 artifact_quota`; metadata/accounting remain unchanged and a smaller upload succeeds. Quota rejection of a derived output also leaves metadata unchanged and no run reserve.
16. Mock transcription receives a real extracted WAV once. Three model turns plus one second of transcription settle 405 microcredits: 330 model credits and 75 transcription credits. These are internal API-tariff calculations, not an external paid bill.
17. Explicit transcription rejection charges no audio credits. A dropped receipt retains a 150-microcredit hold and enters reconciliation. API restart preserves it; premature resume returns 409. An evidenced duration receipt settles 75 credits, resume completes, and no second transcription dispatch occurs.

## Initial attempts and fixture corrections

The new fixture initially needed session-key ownership and public CA permissions for API UID 10001. A requested subnet collided with an existing local network; Docker-assigned isolation resolved it. No machine CA store was changed.

Early assertions assumed normalized PDF/video metadata and fixed metadata envelopes; the actual operation-specific schema was used instead. A repetitive compressed PPTX exceeded the documented ratio bound; the long-slide fixture was changed to stored ZIP entries. Header checks now use HTTP case-insensitivity; GZIP extraction follows the returned member name. These were fixture corrections, not product patches.

The first crash check followed Docker kill STOP/CONT, which marked the container manually stopped and suppressed restart policy. Cancellation now uses pause/unpause, then SIGABRT verifies automatic restart. The original attempt remains a local recorded fixture failure.

A derived-quota attempt used a QA token unsupported by the sibling service's dynamic listing. It safely paused with `module_discovery_unavailable` before inference and spent zero credits. That run was cancelled. The accepted quota check uses a caller supported by the fixture. This does not prove live Zitadel behavior.

Private fixture helpers and full local traces remain in `management.local/files-api-qa-20261012`, excluded from Git. No configs, access tokens, keys, raw logs or provider bodies are copied into this report.

## Remaining boundaries

- No real transcription/vision inference or subjective quality acceptance. No paid calls.
- No OCR, office layout/formula rendering, EPUB spine-order guarantee or EXIF orientation adjustment; these remain documented feature limits.
- PDF embedded-image extraction is checked here with an empty-image PDF; nonempty extraction retains its earlier component fixture coverage.
- No large corpus of complex real office/media files, sustained multi-user load, worker OOM envelope or production network/storage acceptance.
- Authorization uses a synthetic issuer. Existing separate Zitadel acceptance results are not reclassified as new results.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
## Subsequent policy change

The 21 PASS groups above describe the baseline file-operation acceptance. The user subsequently required all incoming/outgoing files to be temporary (default 24 hours) and external-ID refetch through source services. Baseline results do not establish acceptance of that lifecycle change. Its separate evidence is recorded in the temporary-file acceptance report.
