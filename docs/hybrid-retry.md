# Hybrid provider retry

Provider retries use a short local wait, followed by a durable River schedule when more time is required. Only explicit rejections before inference qualify:

- HTTP 429 with `rate_limit_exceeded`, `rate_limit`, or `slow_down`.
- HTTP 503 with `server_is_overloaded`.

Authentication, billing, quota, spend-limit, unrecognized errors, and responses with output do not use this retry path. A transport timeout or lost response remains subject to the separate platform-sponsored unknown-usage policy. Tools and write operations are not repeated by this policy.

## Configuration

The execution configuration defaults are:

```yaml
execution:
  provider_retry_attempt_limit: 6
  provider_retry_window_seconds: 300
  provider_retry_short_limit: 2
  provider_retry_short_budget_ms: 3000
```

Set the attempt limit and short limit to zero to disable new automatic provider retries. An existing persisted schedule still prevents early dispatch. The configured maximums are 20 rejected attempts, a 3600-second episode window, two short retries, and a 3000-millisecond short budget. `Retry-After` accepts seconds or an HTTP date and is a minimum wait. Representable long waits are retained; positive numeric overflow saturates at the largest duration instead of allowing early retry. Backoff adds bounded exponential jitter. The short budget limits waiting and retry dispatch. An accepted request keeps the normal inference timeout and run cancellation context; this budget does not truncate its response.

## Durable scheduling

A short retry keeps the existing reservation and request ID. Only the final verified response is settled. A rejection that cannot be retried locally atomically settles the step with zero usage, stores the rejected-attempt count, deadline, sanitized code, and next attempt time, and releases the run lease. River snoozes retryable queued work so the worker slot is available to other runs; exhaustion pauses the run.

After a restart, the engine checks the stored schedule before it claims the run. Claim rechecks the schedule under the run lock, so duplicate deliveries and stale prechecks cannot bypass the provider wait. The episode deadline gates new dispatch and scheduling; an accepted request keeps its normal inference and run deadlines. A resume checks authorization again and obtains a new fence. Exhaustion pauses the run with `provider_retry_limit`. Explicit resume starts a new episode after the stored provider wait is due and records an audit event. An earlier resume of any paused provider-retry episode returns HTTP 409 `retry_not_due` and leaves the run paused, including when the provider wait exceeds the episode deadline. Pause, cancellation, and run deadlines still apply. A known rejection received before interruption retains its attempt count and provider wait. Paused resume honors that schedule; cancellation remains terminal and does not queue another retry. A verified success closes the retry episode in the same transaction as usage settlement. Schedule and recovery events retain the audit record. A later outage starts a new bounded episode; the normal run lifetime, budget, and step limits remain in force.

## Operator recovery

For `paused/provider_retry_limit`, read diagnostics and the persisted schedule. Correct the rejection cause and wait for `next_attempt_at` before sending resume with a stable idempotency key. A rejected early resume does not clear the wait. Read the run after a lost resume receipt; retry the same command/key rather than creating another run. See [operations](operations.md).

No circuit breaker is implemented. The policy applies to model calls, history summaries, text compaction, and native compaction. It does not add retries to other dependencies or establish production readiness.
