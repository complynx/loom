# Identity cache acceptance

Scope: positive Zitadel-authority cache and emergency administrative purge. No production deployment, IAM changes or paid provider calls.

## Verified locally

- Deterministic HTTP IAM: repeated requests reuse claims during outage; cold/expired entries fail closed; TTL 0 disables reuse; token expiry caps cache lifetime.
- Fresh authorization rejects revoked authority and invalidates prior claims. A failed fresh request does not preserve its old positive entry. Returned permission maps cannot mutate cached roles.
- Actor/subject tokens cache separately. Removed deployment binding denies immediately even when both tokens are warm.
- Concurrent same-token requests share one fill; cancelling its first waiter does not cancel another waiter.
- A saturated 64-fill cache stays saturated after purge until the invalidated fills finish; the 65th cold request is rejected, and capacity is available again after completion. A separate active-fill counter preserves this bound even when the generation changes.
- Shared-generation purge invalidates two authenticator instances. An introspection response arriving after purge cannot repopulate the cache. Failed generation reads deny warm-cache use.
- HTTP purge requires current online authority, settings.write and admin organization; it is permitted in recovery mode. Revocation returns 401 and issuer outage returns retryable 503 without invoking the purge backend.
- Real PostgreSQL: additive migration creates generation 0; concurrent purges yield distinct committed generations without lost updates. Existing schema-isolated store tests run against an owned disposable PostgreSQL 17 container with tmpfs and no durable volume.
- Windows identity/config/API/telemetry/app/store behavioral tests PASS. Full Linux race/integration suite PASS; Golden lint 0 findings. Linux includes pinned Poppler/FFmpeg/fonts, Go 1.27.2 and CGO race instrumentation.
- Two fresh, no-history independent textual reviews: no actionable findings in either pass. A self-review found the invalidated-fill capacity gap between passes; the counter fix and behavioral saturation test were included in the second review. The Git index remained unchanged throughout both reviews.

## Acceptance boundaries

The full native Windows suite reports one FAIL in the unchanged `TestPDFRenderRejectsUnresolvedFont`: local Poppler renders a page successfully despite the empty FONTCONFIG_FILE fixture, so the expected missing-font error is absent. The new authorization tests and other packages passed. The pinned Linux worker environment passed this PDF test. This is recorded as a native media-fixture acceptance failure, not silently relabelled as a complete Windows PASS.

The new cache contract uses deterministic IAM responses, not a new live Zitadel outage/revocation acceptance. Historical real-Zitadel tests remain evidence for their original online-only configurations. Target deployment, sustained load, 10,000-entry eviction acceptance and multi-process target-runtime acceptance remain pending. The cache is bounded by entry count, not a measured resident-memory budget.

Purge invalidates positive permission claims only. Discovery/JWKS caches, stored sessions, module dynamic listings and existing authorized work have separate lifecycles. PostgreSQL remains required for cache generation checks. Ordinary upstream revocation may be delayed by the configured TTL; sensitive boundaries require online authority.

QA containers use the `loom-identity-cache-qa-*` prefix. They are removed after verification. The existing Shuttle/Zitadel stand is not modified.

Written by Codex (gpt-6/Codex desktop), developer of Loom
on behalf of Daniel Drizhuk
