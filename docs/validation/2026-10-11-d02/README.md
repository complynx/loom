# D02 local verification — 2026-10-11

Status: local PASS. Runtime deployment and full backup/PITR rehearsal remain in
C02. This result does not establish production acceptance or installed Zitadel
compatibility. No production resources, neighboring repositories, paid providers
or external issue trackers were changed. The disposable database container and its
temporary credential file were removed after verification; shared build caches remain.

## Change

- Previous AES256 keys can read persisted session envelopes; only the active key
  writes new envelopes. Existing single-key configurations remain valid.
- `rotate-session-keys --batch-size 100` commits one bounded database batch.
  Cancellation or corruption rolls back that batch. Repeated commands resume.
- Cryptographic migration preserves expiry, revocation and authorization version.
  Row locks serialize migration with reauthorization and revocation.
- Startup checks keys referenced by every stored session, including expired and
  revoked records. It refuses a configuration missing a required historical key.
- [Operator procedure](../../session-key-rotation.md) specifies the two-phase
  rolling transition, old-writer barrier and separate backup key retention.

## Behavioral evidence

[Linux race/integration output](linux-race.txt): Go 1.27.2, Linux amd64, disposable
PostgreSQL 17. Full `go test -race -tags=integration ./...` passes. SQL tests use a
real database; issuer and model integration use local fixtures.

New coverage verifies old/new reads during rolling transition, reads after
restart without the old key, unknown-key denial, authenticated metadata tamper
rejection, expired-envelope migration without renewed authorization, missing-key
startup guard, bounded progress, interrupted batch rollback, atomic corruption
failure, preserved revocation/version and concurrent reauthorization.

[Golden lint output](lint.txt): pinned golangci-lint 2.14.0 reports zero issues.
`git diff --check` passes. No dependencies or database schema were added.

## Independent review

One fresh, no-history, read-only reviewer inspected the current feature diff and
source context, including the concurrency test. Result: **No actionable findings**.
Reviewed cancellation, expiry/revocation, credential binding, row locking and
rolling key removal. The Git index remained empty throughout review; no staging,
commit, push or deployment was performed in this stage.

The database key guard cannot inspect historical backup contents or stop an old
process that still writes old-key envelopes. Follow the deployment barrier and
retain historical key versions. Actual backup restoration remains an operational
acceptance gate, not a claim made by these tests.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk