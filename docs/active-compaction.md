# Active-run compaction and provider recovery

Compaction replaces the model input window. Full chat history, tool effects, approvals, permissions and billing remain separate durable records in PostgreSQL.

## Replacement windows

For the OpenAI Responses transport, Loom calls stateless `POST /responses/compact`. It saves the returned window, including the encrypted compaction item, as a completed `compact` step. Embedded image, file and audio content is replaced with a reminder to reopen the artifact or fetch it again. The encrypted item remains intact. Compaction output is context; it never dispatches tools.

Other transports use a billed text summary plus recent user text. Compaction runs between model turns, after every outstanding tool call has a saved result. A completed checkpoint is reused after restart without another provider call or charge for that step.

The preset trigger is 244800 tokens and the hard input ceiling is 272000. The 204000 history target does not set the native endpoint's output size. Loom counts the complete request, including instructions and tools, when needed near the trigger. Opaque compacted windows require provider token counting. Ordinary text can use conservative byte bounds.

The run pauses if the input exceeds its hard limit, if an opaque window cannot be counted, or if a counted replacement still reaches the compaction trigger. It does not repeatedly compact an unchanged checkpoint.

## Reservation and settlement

The compact endpoint has no `max_output_tokens` parameter. Loom therefore reserves output cost for the larger of the model context window and the input ceiling, using the run's model tariff. This can require much more credit than ordinary inference. The reservation is a bound, not the expected charge. Settlement uses actual input, cached input, cache-write and output usage.

See the [OpenAI compaction contract](https://developers.openai.com/api/docs/guides/compaction) and [preset tariffs](openai.md).

## Network errors and restarts

`execution.sponsored_attempt_limit` defaults to `2`. For a lost inference response with unknown usage, this policy releases the user's reservation and records possible provider liability in `platform_provider_attempts`. The successful replacement response uses normal user billing. After one automatic recovery, a second uncertain response releases its reservation and pauses the run with `platform_retry_limit`. The limit covers the whole run, including summaries and compaction. Zero disables sponsorship and leaves unknown outcomes for operator reconciliation.

Expired-worker recovery requires fresh authorization and a transaction that checks the lease, fence, run state and outstanding effects. It excludes active leases, client pause/cancel requests, incomplete tools and uncertain service writes. A client interruption is not a sponsored network failure. Saved output is replayed; a missing response cannot dispatch tools.

Platform exposure is an estimate of possible cost. Automatic settlement against late provider receipts, a global platform spend ceiling and a dependency circuit breaker are not implemented. Known provider rejections with no output use the separate [hybrid retry policy](hybrid-retry.md), which handles Retry-After, jitter and persisted schedules.

Source: `internal/runs/compaction.go`, `internal/runs/compaction_files.go`, `internal/runs/provider_recovery.go` and `internal/store/platform_attempts.go`.
