# Transcription limits

[Guide index](../file-tools.md)

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
