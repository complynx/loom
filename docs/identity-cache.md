# Identity permission cache

`identity.permissions_cache_ttl_seconds` defaults to **120** when omitted. Set **0** to disable; the maximum is **300**. Restart Loom after a configuration change. The cache holds verified positive token claims in each process, keyed by SHA-256 of the exact token. Raw credentials are not stored in cache entries or metrics. Different actor and subject tokens use separate entries. Each process retains at most 10,000 entries and evicts the least recently used entry when full. It permits at most 64 concurrent normal cache fills; a cold request that needs another fill returns retryable 503. Concurrent requests for the same token share one bounded fill. Cancelling one waiter does not cancel the others.

TTL is absolute, starts when verification completes and never slides on reads. It cannot exceed introspection `exp` or a signed JWT's `exp`. Denials and errors are not cached. There is no stale-on-error extension. A warm entry can cover a short Zitadel outage; a cold or expired entry requires online verification and returns retryable 503 if the issuer is unavailable. Normal IAM role/token revocation can therefore take up to the configured TTL to appear.

Loom does not cache final permission decisions. Local session revocation, deployment caller bindings/classification, user policy, settings and run ownership remain checked by their existing boundaries. A cached role cannot bypass a removed local binding. Cache is empty after restart; it is not a durable offline identity store.

## Fresh boundaries

The following operations bypass cached claims for both actor and subject:

- Administrative mutations, including permission-cache purge, policy, billing and recovery changes.
- Session creation and reauthorization.
- Run input (which can contain approval) and resume.
- External module write preparation/dispatch principal refresh and paid transcription preparation/execution.

Fresh checks neither reuse nor populate the positive cache. They invalidate that token's prior entry and pending normal fills after completion, including on denial or network failure. Defensive local session revocation and pause/cancel remain usable with valid cached credentials. Ordinary inference admission and reads can use cached authority. A fresh check still uses the separate, bounded JWKS cache for signature keys.

## Emergency purge

`POST /v1/admin/identity/cache/clear` takes no body and requires **settings.write**, the configured **admin organization**, and fresh online authentication. Success is `200 {"generation": N}`. Missing backend or failed authorization dependency returns retryable 503; wrong permission/organization returns 403. Recovery mode permits this repair operation. The endpoint is part of the generated OpenAPI reference.

The purge invalidates **all positive permission entries**, across API and workers that share this database. Migration `00004_identity_cache.sql` adds one singleton generation row. Each token check reads that generation; cache fills read it again before publication. An old pending response cannot repopulate a purged generation. PostgreSQL is therefore still required even for a warm entry. Failure to read the generation fails closed with retryable 503. This trades a small SQL read for cross-process invalidation without an extra broker.

Purge does not cancel work already authorized, remove stored sessions, revoke IAM tokens, or clear discovery/JWKS/module/content caches. If the issuer still grants a role, a new online check can grant it again. For emergency revocation: remove the IAM grant/token, then purge. If Zitadel is unavailable, this endpoint cannot prove current administrative authority; use existing local session-revocation controls where applicable or stop workers. Restoring an old database snapshot requires restarting all Loom processes under the existing recovery procedure.

## Rollout

Apply migrations using the migration principal before starting the new binary. Ensure the runtime database principal has `SELECT, UPDATE` on `identity_cache_state` (existing all-table grants/default privileges may already provide them). Do not grant clients direct access to this row. Deploy API and workers from the same reviewed version. No Redis, new secret, or additional IAM role is required. Set TTL to 0 and restart for the online-only fallback; the generation table is still required. Rollback can leave the additive table in place; remove it only after all new binaries stop.

## Visibility and verification

Prometheus exposes `clx_identity_cache_total{result="hit|miss|joined|fresh|denied|error|cleared"}` and `clx_identity_cache_age_seconds{cache="permissions"}`. A request can contribute both a lookup and an error outcome; these are event counters, not mutually exclusive request counts. Age measures reused claims, not dependency latency. Existing authorization spans/logs cover authentication duration and failures; successful API purge emits an info event with the generation. No token/subject labels are emitted.

The repository's cache tests cover outage reuse, expiry, disabled cache, fresh revocation, permission-map isolation, concurrent fill/cancel, cross-instance purge, in-flight purge rejection, fail-closed generation reads, administrative gates and atomic PostgreSQL generations. These tests do not establish outage or load acceptance for a deployment.
