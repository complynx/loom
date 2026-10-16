# Transcription limits

[Guide index](../file-tools.md)

## Paid audio

`audio_transcribe({handle_id,language?,prompt?,start?,end?})` is a standalone model function, outside JS. It accepts an audio or video handle. `start` and `end` are seconds in the original source. Without `end`, extraction selects up to 300 seconds from `start`; deployment policy can impose a lower maximum. When that maximum is lower, set an explicit range that fits it. The result's `next_cursor` is the next numeric `start` for the original source.

Enable the private worker first, then set `transcription.enabled:true` in [the config fragment](../../examples/file-worker.config.yaml). Configure the provider credentials in the API process. Configuration supports the duration-billed `gpt-transcribe` and `whisper-1` model names; other models are rejected. The provider abstraction is `transcription.Provider`; the OpenAI implementation streams multipart audio without automatic retries. Timestamp segments are returned only when the provider supplies them.

Before dispatch, Loom extracts a bounded WAV, records the configured tariff in a durable transcription step and reserves credits. `transcription.credits_per_second` is an operator-configured tariff, in microcredits per second. At the example rate of 75, a 60-second receipt costs `ceil(60 × 75) = 4500` microcredits, or $0.0045 at 1 USD = 1,000,000 microcredits. This describes Loom's configured charge, not a guarantee of the provider's current price. Set and review the tariff for the selected provider model.

The reserve includes one second of duration tolerance. Settlement uses an explicit provider duration receipt, rounds its duration up to milliseconds, then rounds the aggregate charge up to one microcredit. A missing, invalid or out-of-reserve receipt is an uncertain outcome.

A completed step replays its saved transcript without another paid call. Replay rechecks current artifact permissions and the original source disclosure policy; a revoked or unavailable source pauses before the next model call. A definite pre-dispatch failure or rejection settles zero. An uncertain dispatch retains its reserve and enters reconciliation. Operator receipt reconciliation uses the frozen tariff and restores a valid function-call output before resume. Do not retry an uncertain paid step as a new call. `X-Client-Request-ID` carries the step ID for correlation; it does not provide provider idempotency.

The result includes bounded `text`, `segments`, `start`, `end`, `next_cursor`, `transcript_artifact_id` and `transcript_handle_id`. The full transcript is saved as a temporary text artifact. Preview text is at most 32 KiB. Segments are capped at 100 and omitted if their serialized size exceeds 32 KiB. `partial` describes preview truncation; use `next_cursor` to determine whether more source audio remains. Read a longer transcript with `modules.files.text_read` using the transcript handle.

Revocation or persistence failure after paid dispatch can require reconciliation. The reserve remains held to avoid charging for an automatic repeat. Recovery does not regenerate an expired transcript artifact by repeating transcription.

## Bounds and visibility

File-tool input is at most 20 MiB. Worker jobs have a 30-second ceiling and at most two concurrent slots; API timeouts can be lower. Derived output streams total at most 64 MiB per job and also consume artifact quota. Format-specific text responses and image dimensions have smaller limits described in [Methods](methods.md).

Archive expansion is bounded to 20 MiB, 1000 entries and a 200:1 compression ratio. These checks also apply to ZIP-based office formats. Large reads return `partial` and `next_cursor` where the format supports paging. A partial JSON summary has no continuation cursor; use a narrower JSON Pointer. Some oversized CSV/XLSX rows return a bounds error instead of a page. Reduce the selected range or choose a bounded raw-text view where available.

Readiness probes the worker when file tools are enabled outside recovery mode. Metrics report `file_processor` availability and operation counts/duration for `file_processing` and `transcription`. Logs identify status and runs without logging raw file content or provider credentials.

Not implemented: OCR extraction, PDF table reconstruction, office layout rendering or formula execution, subtitle translation, realtime transcription, video editing and archive-wide bulk extraction. Scanned PDF pages can be rendered and attached for model vision.

## Live execution limits

With `settings.read`, GET `/v1/admin/settings` returns current settings and version. With `settings.write`, PATCH the full settings document using that version. See the [API reference](../api.md) for request details.

`total_active_run_limit`, `script_timeout_ms`, `parallel_rpc_limit` and `context_input_token_budget` are live bounds, capped by deployment ceilings. Admission is serialized with limit updates. Active runs retain their admitted snapshots; each next script or model turn uses the stricter current bound. Raising a limit does not enlarge an admitted run's snapshot. Lowering a limit does not interrupt work in progress. Context that cannot fit safely pauses before another paid model request. Legacy zero-valued fields normalize to deployment defaults.
