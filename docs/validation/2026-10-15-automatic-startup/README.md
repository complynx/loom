# Automatic startup acceptance

Local Linux/Docker and isolated PostgreSQL 17 acceptance; no production deployment,
IAM mutation or paid provider calls. Operator-specific configuration is outside
this repository. Application behavior is described in [startup](../../startup.md).

Verified:

- Concurrent and repeated application/River migration calls succeed on an empty
  schema. Future application schema versions prevent upgrade/downgrade. A canceled
  advisory-lock wait returns without creating migration tables.
- The normal `Serve` runtime test starts without pre-applied schema or a supplied
  encryption key, executes a queued request through the real River/subprocess
  path with fake HTTP peers, and shuts down.
- Concurrent initial key creation yields one complete stable key. Restart
  preserves it. Corrupt keys, missing keys with stored sessions, and key creation
  in recovery mode fail without replacement.
- Application JSON credentials resolve from base64 or a bounded file. Conflicting
  sources, wrong client IDs, invalid encoding and service-account JSON are denied.
  Fake introspection verifies RS256 signatures, key IDs, client identity, issuer
  audience, short expiry, unique assertion IDs and absence of HTTP Basic auth.
- Default startup migration setting and explicit opt-out are tested.
- An offline read-only container prepares a fresh data volume; UID/GID 10001 can
  write its private keys/artifacts directories afterward with all capabilities
  dropped. Ownership is 10001:10001 and mode is 0700.
- Operator-local latest rollout harness passes success, invalid configuration,
  failed pull, mismatched revision, failed recreate and missing readiness cases.
  Failures before stop retain the previous API. Compose structure validates.
- Full `go test -race -parallel=4 -tags=integration ./...` passes in pinned Linux
  Go/PostgreSQL/media environments. Golden lint reports zero issues. CI actionlint,
  whitespace and 488 relative documentation links pass.
- Two fresh independent static reviews report no actionable findings. The Git
  index remains unchanged during review.

Boundaries:

- This change has not been accepted against a live Zitadel application JSON key,
  the target ingress, GitHub secret forwarding over SSH, or the production DB
  owner. Cryptographic protocol checks use a fake introspection server.
- Schema failure and future-version behavior is checked locally; there is no
  promise of compatibility with every historical backup or of automatic rollback.
- All API replicas must share session encryption keys. Automatic key generation
  does not coordinate independent replica volumes.
- The full native Windows test attempt fails the existing unresolved-PDF-font
  case with the host's media tools. Linux with the pinned Poppler/font packages
  passes. Linux/Docker remains the target runtime.

## Shared rollout and container readiness

The local operator deployment now calls the shared `scripts/rollout_all.sh` for
Loom, like other services. No Loom rollout script, env overlay or empty env file
is required. Compose injects the three secrets and keeps the data-preparation
completion dependency. Automatic migrations and key checks remain in API startup.
API Compose healthchecks call `loom healthcheck`; no shell or curl is needed in
the scratch API image. Five minutes of startup grace accommodate migrations.

Validation for this change:

- `go test ./cmd/loom`: passed.
- Golden lint for `./cmd/loom/...`: zero issues.
- Linux amd64 binary exercised in a network-isolated disposable container:
  readiness 200 succeeds; 503, redirect and unavailable listener fail.
- Production Compose renders with dummy secrets and no extra env file.
- Both prepared receiver/SSH workflows pass actionlint together.
- One independent read-only review: no actionable findings. Nothing staged.

Limits: shared rollout does not wait for healthy. Docker records unhealthy but
does not restart solely for that status. Operators verify `/readyz` after rollout.
The old external image-revision equality gate is removed, not reproduced inside
the API; container readiness checks dependency availability instead. No production
deployment or paid provider calls were made. The disposable QA container was
removed; cached shared images remain available.