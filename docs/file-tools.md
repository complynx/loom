# File tools and paid transcription

Enable `file_tools` only with the private worker described in [file-worker.md](file-worker.md). Merge [the config fragment](../examples/file-worker.config.yaml) into the API configuration. Keep provider credentials in the API container. The worker receives a stream of one file and has no network, database, credential or artifact-volume access.

## Handles and access

Use `modules.artifacts.open({artifact_id})` to obtain a handle for the current run. All `modules.files` methods take `handle_id`. Output files return `artifact_id`, temporary `handle_id`, MIME and size. No binary payload or filesystem path enters JS. Durable artifact IDs let a later run obtain a fresh handle. Owner, organization, run, handle expiry, live permissions and disclosure policy are checked. Derived files inherit the source disclosure flag and service provenance, consume the owner's quota and are referenced by the chat. File text is untrusted data.

Use `artifact_attach` to send a derived image to a model that supports images. Services can download artifact bytes through the existing authorized streaming artifact endpoint. Do not use base64 script reads for large files.

## Methods

- `info`: stored MIME, size and checksum.
- `pdf_info`, `pdf_text`, `pdf_search`, `pdf_render`, `pdf_images`: metadata, text, literal/RE2 search, page/crop PNG rendering and embedded images. Use `page`, `end_page`, `offset`, `max_chars`, `query`, `regex`, `width`, `height`, `x`, `y` as appropriate. Text cursors are byte offsets; offsets must be UTF-8 boundaries. Too-small text limits return an error instead of a nonadvancing cursor. Search is line-scoped and advances only past complete lines; `max_chars` is a soft scan budget. Lines at least 64 KiB are skipped with explicit `skipped_long_lines` and `line_limit_bytes` fields. Search excerpts are bounded; use text reads to inspect surrounding content.
- `image_info`, `image_resize`, `image_crop`: JPEG/PNG/GIF first-frame metadata, resize and crop. Resize defaults to `max_edge:1280`; crop uses `x,y,width,height`. Stored-raster coordinates apply: EXIF orientation is not applied. For animated GIF use video methods.
- `video_info`, `video_storyboard`, `video_frame`, `video_audio`: metadata, sampled frames, one timestamped frame and mono 16 kHz WAV extraction. Set `start,end,interval,width,max_frames` for a storyboard; use a smaller interval for a detailed section. Frame reads use `timestamp`. Storyboards have at most 24 frames. Follow `next_cursor` as the next `start`. Audio extraction has at most 300 seconds per chunk; inspect `has_audio` first.
- `text_read`, `text_search`: UTF-8/UTF-16 reading and literal/RE2 search. Reads use byte `cursor` and `limit`; search uses `query,regex,cursor,limit` with line cursors.
- `csv_read`, `json_read`, `xml_read`, `html_read`: CSV row pages, JSON Pointer, and bounded XML/HTML text. JSON summaries may be partial without a continuation cursor. HTML does not execute scripts or fetch resources.
- `archive_list`, `archive_extract`: ZIP/TAR/GZIP entries and one named `member` extracted into an artifact. Traversal, unsafe entries, excessive expansion and duplicate ZIP entries are rejected.
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
