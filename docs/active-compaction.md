# Active-run compaction and provider recovery

The full chat history and execution checkpoints stay in PostgreSQL. Compaction replaces only the model input window. Tool effects, approvals, permissions and billing remain separate durable records.

OpenAI uses stateless `POST /responses/compact`. The returned window, including its encrypted compaction item, is saved as a completed `compact` step and reused without pruning. Other transports use a paid text summary with recent user text. Compaction runs between model turns, after every outstanding tool call has a saved result. It does not execute output items returned by the compact endpoint.

The per-model trigger stays `244800`; hard input stays `272000`. `204000` remains the retained-history target. The native endpoint selects its output size; this is not a request for exactly 204000 or 45000 tokens. Provider token counting includes instructions and tools when needed near the trigger, and is required for opaque compacted windows. Byte estimates remain conservative for ordinary text. A window that exceeds the hard limit pauses rather than being silently truncated.

The compact endpoint has no `max_output_tokens` parameter. Admission therefore reserves the full possible output window at the model's production tariff. Settlement uses actual input, cached input, cache-write and output usage. A saved successful checkpoint is not paid again after restart. The conservative reservation can require a larger run budget than ordinary inference.

## Network errors and restarts

`execution.sponsored_attempt_limit: 2` is the deployment default. A lost inference response releases the user's reserve and records possible provider liability in `platform_provider_attempts`. The successful replacement response uses normal user billing. After one automatic recovery, a second uncertain response pauses the run with `platform_retry_limit`; it releases that reserve too. The limit covers the whole run, including summaries and compaction. Zero disables this policy and retains operator reconciliation.

Expired worker recovery requires fresh authorization and a transaction that checks the lease, fence, run state and outstanding effects. Active leases, client pause/cancel, incomplete tools and uncertain service writes cannot use this recovery. A client-requested interruption is not classified as a platform network failure. Completed output is replayed from storage; missing output never dispatches tools.

Platform exposure is an estimate, not a claim that OpenAI charged that amount. Automatic settlement against late provider receipts, a global platform spend ceiling and a dependency circuit breaker are not implemented. Operator reconciliation remains available for uncertain outcomes outside the sponsored policy. Known output-free provider rejections use the separate [hybrid retry policy](hybrid-retry.md), with Retry-After, jitter and persisted schedules. It does not turn unknown transport errors into known zero-cost attempts.

## Evidence and source

Behavioral tests: `integration/active_compaction_test.go`, `integration/provider_recovery_test.go`, `internal/store/platform_attempts_test.go`, `internal/provider/compact_test.go`. They cover native and fallback windows, call/result boundaries, question/resume, restart, lost replies, bounded retries and revoked authorization. They use test providers and isolated PostgreSQL; live paid OpenAI acceptance remains open.

Compared with the current public Codex source: [remote compaction](https://github.com/openai/codex/blob/main/codex-rs/core/src/compact_remote_v2.rs), [local compaction](https://github.com/openai/codex/blob/main/codex-rs/core/src/compact.rs). Codex retains bounded recent context; it does not specify a fixed final 45k window. This implementation uses the public [Responses compaction contract](https://developers.openai.com/api/docs/guides/compaction), rather than Codex-specific transport extensions. [OpenAI retry guidance](https://developers.openai.com/api/docs/guides/rate-limits) requires bounded retries, server delay handling and separate treatment of quota errors and streamed output.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
