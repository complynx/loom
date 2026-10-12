# File tools and paid transcription

Enable `file_tools` only with the private worker described in [file-worker.md](file-worker.md). Merge [the config fragment](../examples/file-worker.config.yaml) into the API configuration. Keep provider credentials in the API container. The worker receives a stream of one file and has no network, database, credential or artifact-volume access.

## Handles and access

Use `modules.artifacts.open({artifact_id})` to obtain a handle for the current run. All `modules.files` methods take `handle_id`. Output files return `artifact_id`, temporary `handle_id`, MIME and size. No binary payload or filesystem path enters JS. Physical artifact content is valid only until `expires_at`; a later run may open a fresh handle while the file is live. Service-backed IDs can also serve as stable references through the recovery contract below. Owner, organization, run, handle expiry, live permissions and disclosure policy are checked. Derived files inherit the source disclosure flag and service provenance, consume the owner's quota and are referenced by the chat. File text is untrusted data.

Use `artifact_attach` to send a derived image to a model that supports images. Services can download artifact bytes through the existing authorized streaming artifact endpoint. Raw base64 reads through `modules.artifacts.read` are removed. Use bounded text/document tools for extracted text; image attachment and HTTP content streaming bypass JS.


## Temporary files and external IDs

All uploaded, imported, and generated files are temporary. Set `artifacts.ttl_seconds` in deployment YAML. Default: 86400 (24 hours); zero also uses the default. Valid explicit range: 1–604800 seconds. There is no forever option. The deadline is fixed at creation and is not extended by reads, active chats, handles, restarts, or run resumes. Derived files use the earlier of their own deadline and the source deadline. A handle expires at the earlier of 15 minutes and its file deadline.

Access is denied at the deadline, before background collection. A minute-based lifecycle sweep tombstones expired files, including files referenced by active runs. Collection deletes bytes and then releases quota. Sweeping and collection use separate minute ticks, so healthy physical removal can lag by roughly two minutes; backlog, database/disk outages, or recovery mode can delay it further. Metadata, provenance, financial receipts, chat messages, and extracted text already used in a conversation remain under their own policies. File bytes are excluded from long-term backups. Compaction checkpoints replace inline image/file/audio parts with text markers; migration 00002 also removes legacy inline attachments and legacy base64-read results. Opaque provider compaction context remains provider-generated context.

Fetch an external file through a registered read method:

```js
const fetched = await modules.artifacts.fetch({
  module: "fixture", method: "fetch_file", external_id: "fixture-file"
});
const file = fetched.artifacts[0];
const handle = await modules.artifacts.open({artifact_id: file.artifact_id});
return await modules.files.text_read({handle_id: handle.handle_id, limit: 2048});
```

The service read method accepts `{external_id}`. Its normal protocol reply contains `result` plus `artifacts` descriptors with `external_id`, `download_path`, `mime_type`, `size_bytes`, and `sha256`. The registered method declares allowed MIME types and permissions in its manifest. The registry delegates current user credentials, checks dynamic permissions and service freshness, downloads from the same registered service without redirects, verifies size/checksum/MIME, and returns temporary internal IDs plus `source_service_id`, `source_external_id`, and `expires_at`. Raw URLs, user credentials and filesystem paths are not fetch arguments. Write methods cannot be called through this read broker.

External IDs and versions are owned by the source service. Use immutable/versioned IDs. A descriptor can declare `fetch_method`: an authorized read method in the same module accepting `{external_id}` and returning exactly one file. Loom retains its source service/module/method, external ID and immutable checksum, MIME and size as metadata. Agent file opens, initial model attachments and attachment replay can resolve the original internal ID after expiry or missing local bytes. The authorized service supplies a new temporary artifact; the original reference remains stable. HTTP metadata/content and ordinary HTTP handle creation still enforce the physical artifact deadline; use the current ID or fresh agent handle for streaming.

```js
const file = modules.artifacts.file({
  module: "fixture", method: "fetch_file", external_id: "fixture-file"
});
const page = await file.call("text_read", {limit: 2048});
return {file: file.toJSON(), page};
// Later script: modules.artifacts.file(saved.file).call("text_read", {...})
```

`file.id` is the stable reference; `file.currentId` is the latest physical artifact. `file.handle()` obtains a run-scoped handle. `file.call(method, options)` injects that handle into a bounded `modules.files` call. Serialization contains source IDs and the stable artifact ID, without bytes, credentials or handles. Concurrent helper calls share the pending open; independent callers share a database refresh lease. Refresh leases last two seconds with a 500 ms heartbeat; resolution is bounded by 30 seconds and the calling script/request deadline. No database transaction stays open during download.

Current ownership, live chat references, module permissions and both source/fetch disclosure policies are checked, including cached reuse. A changed checksum, MIME or size returns `file_version_changed`; initial model attachment recovery pauses the run with that reason. Revoked access and unavailable sources fail safely. Expired handle metadata is retained for nonterminal runs solely for private checkpoint recovery; public handle content remains expired. Recovery does not rerun paid transcription or uncertain effects.

Raw uploads, derived files without a source refetch contract, and legacy imports without `fetch_method` cannot be recovered automatically. Supply them again when needed. Migration 00003 adds reference metadata; it does not guess legacy source methods. Deleting all chats referencing a file prevents resurrection. The acceptance service exposes `fixture.fetch_file` for `fixture-file` and `fixture-image`.

## Methods

- `info`: stored MIME, size and checksum (direct metadata object, without a `data` wrapper). `pdf_info.data.info` is native pdfinfo text; `video_info.data` is native FFprobe JSON.
- `pdf_info`, `pdf_text`, `pdf_search`, `pdf_render`, `pdf_images`: metadata, text, literal/RE2 search, page/crop PNG rendering and embedded images. Use `page`, `end_page`, `offset`, `max_chars`, `query`, `regex`, `width`, `height`, `x`, `y` as appropriate. Text cursors are byte offsets; offsets must be UTF-8 boundaries. Too-small text limits return an error instead of a nonadvancing cursor. Search is line-scoped and advances only past complete lines; `max_chars` is a soft scan budget. Lines at least 64 KiB are skipped with explicit `skipped_long_lines` and `line_limit_bytes` fields. Search excerpts are bounded; use text reads to inspect surrounding content.
- `image_info`, `image_resize`, `image_crop`: JPEG/PNG/GIF first-frame metadata, resize and crop. Resize defaults to `max_edge:1280`; crop uses `x,y,width,height`. Stored-raster coordinates apply: EXIF orientation is not applied. For animated GIF use video methods.
- `video_info`, `video_storyboard`, `video_frame`, `video_audio`: metadata, sampled frames, one timestamped frame and mono 16 kHz WAV extraction. Set `start,end,interval,width,max_frames` for a storyboard; use a smaller interval for a detailed section. Frame reads use `timestamp`. Storyboards have at most 24 frames. Follow `next_cursor` as the next `start`. Audio extraction has at most 300 seconds per chunk; inspect `video_info.data.streams` for `codec_type: "audio"` first.
- `text_read`, `text_search`: UTF-8/UTF-16 reading and literal/RE2 search. Reads use byte `cursor` and `limit`; search uses `query,regex,cursor,limit` with line cursors.
- `csv_read`, `json_read`, `xml_read`, `html_read`: CSV row pages, JSON Pointer, and bounded XML/HTML text. JSON summaries may be partial without a continuation cursor. HTML does not execute scripts or fetch resources.
- `archive_list`, `archive_extract`: ZIP/TAR/GZIP entries and one named `member` extracted into an artifact. Use the returned entry name; GZIP uses the logical member `content`. Traversal, unsafe entries, excessive expansion and duplicate ZIP entries are rejected.
- `office_docx`, `office_xlsx`, `office_pptx`, `office_odf`, `epub_read`: basic text or cached cell values. XLSX takes `sheet,cursor,limit`. PPTX without `slide` uses a zero-based slide cursor and returns bounded slide previews. Select `slide:N` (one-based) to read its full text with UTF-8 byte `cursor,limit` pages up to 32 KiB. `cursor_unit` identifies the mode. EPUB lists archive paths, then reads a chosen `member`; this is not spine ordering. DOCX/ODF/EPUB text cursors are UTF-8 byte offsets.

For video understanding, read a storyboard, attach selected frames, then call `audio_transcribe` on the original handle if audio exists. Repeat with a tighter time range when needed. All files stay outside JS.

## Paid audio

`audio_transcribe({handle_id,language?,prompt?,start?,end?})` is a standalone model function, outside JS. The abstraction is `transcription.Provider`; the production implementation streams multipart audio to OpenAI without automatic retries. Deployment supports duration-billed `gpt-transcribe` and `whisper-1`; token-billed models are rejected by configuration until their admission policy is implemented. Timestamp segments are returned only when provided by the model.

Before dispatch, the system extracts a bounded WAV, freezes the tariff in a durable transcription step and reserves credits. The default example is 75 microcredits per second (1 USD = 1,000,000 microcredits): GPT-Transcribe costs $0.0045/minute in [OpenAI documentation](https://developers.openai.com/api/docs/models/gpt-transcribe), checked 2026-10-12. Configure the correct rate if selecting another model. Reserve includes one second of duration tolerance. Settlement uses the explicit duration receipt, rounded up to milliseconds, then rounds the aggregate charge up to a microcredit.

A completed step replays its saved transcript without another paid call. Replay rechecks current artifact permissions and the original source disclosure policy; a revoked or unavailable source pauses before the next model call. A definite pre-dispatch failure or rejection settles zero. An uncertain dispatch retains its reserve and enters reconciliation. Operator receipt reconciliation uses the frozen tariff and restores a valid function-call output before resume. Unknown transcription is not automatically sponsored or retried. `X-Client-Request-ID` carries the step ID for correlation; it is not a provider idempotency guarantee.

The full transcript is saved as an artifact. Preview text is at most 32 KiB, with bounded segments. Revocation or persistence failure after a paid dispatch can require reconciliation; this preserves the hold instead of risking another paid call.

## Bounds and visibility

Input is at most 20 MiB. Worker jobs have a 30-second ceiling and two concurrent slots. Text responses are bounded; derived output streams are bounded and consume artifact quota. Archive expansion is at most 20 MiB, 1000 entries and a 200:1 ratio. Large text reads return partial results and a cursor where the format supports paging. Some oversized CSV/XLSX rows return a bounded-view error rather than a page.

Readiness checks the enabled worker. Metrics report `file_processor` availability and operation counts/duration for `file_processing` and `transcription`. Logs contain status and run IDs, not raw file content or provider credentials.

Not implemented: OCR text extraction, PDF table reconstruction, office layout rendering/formula execution, subtitle translation, realtime transcription, video editing and archive-wide bulk extraction. Scanned PDF pages can be rendered and attached for model vision.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk

## Live execution limits

With `settings.read`, GET `/v1/admin/settings` returns the current settings and version. With `settings.write`, PATCH the full settings document using that version. Fields `total_active_run_limit`, `script_timeout_ms`, `parallel_rpc_limit` and `context_input_token_budget` are live bounds. Values cannot exceed deployment ceilings. Admission is serialized with limit updates, so concurrent creates cannot overrun a lowered global limit. Active runs keep their admitted snapshots; each next script or model turn uses the stricter current bound. Raising a limit does not increase a previously admitted run's snapshot. Lowering a limit does not interrupt work already in progress. Context that cannot fit safely pauses before another paid model request. Legacy zero-valued fields normalize to deployment defaults.
