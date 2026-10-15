# Bounded disk-full manual QA — 2026-10-11

Scope result: artifact ENOSPC cleanup/recovery, PostgreSQL actual ENOSPC rejection/recovery, read-only rejection/recovery and shutdown checks passed. This is local focused acceptance, not a production release certificate.

Reviewed runtime image: `sha256:cef24f773800be422bcbeed05deb4456afe95edc865a416affbf48947f8ad1c4`. API, migration, sandbox and Docker proxy used this image. Runtime binary SHA256 `2B55B9F4AF17B6456CD7C73DEAAEC83365A3AD42D7EC36001BDC596FC2EBE188`. Fixture explicitly overrides image user to root to access its root-owned tmpfs; production UID/volume permissions are outside this check.

Isolated project `clx-fullqa-disk-20261011`, API loopback58580; fake HTTPS issuer/provider and real PostgreSQL17. PostgreSQL data is capped256MiB tmpfs, artifact root8MiB. Host storage was not filled. All model requests in this report hit the deterministic local fake provider. Three fake calls total; no OpenAI calls.

## Passed behavior

- Small artifact HTTP201; original26-byte content and SHA256 preserved.
- A10MiB raw upload exceeds the8MiB tmpfs while remaining below20MiB API/upload limits. HTTP500, metadata count stays1, only the original blob remains, no .tmp/orphan. Credits remain1000000 available/0spent/0reserved. Small upload after failure returns201 without restart.
- PostgreSQL filesystem filled to262144/262144KiB with0available. Filler dd returns1/No space left. Actual SQL relation growth fails `could not extend file ... No space left on device`; HTTPchat creation returns500 rather than false success.
- Removing only the filler restores HTTPchat201, retained metadata/billing snapshot and original blob unchanged. PostgreSQL did not require a restart in this measured path.
- With PostgreSQL full again, Docker TERM-to-die is437.4349ms, exit0, below5second deadline and1second target. This measurement has no active inference.
- Actual role-level default_transaction_read_only forced fresh application sessions read-only. HTTPwrite500. Reset plus fresh sessions restores HTTPwrite201.

## Full disk can still support allocated-page writes

The supplemental smallrun was durably admitted202 and completed while df still reported100%. Its provider result and110microcredit settlement were stored exactly once. A240000-character stimulus was also durably admitted; it completed one summary step then paused context_unavailable. After diskfree, explicit resume200 stayedpaused without another provider call or charge. Because the pause persists after freeing disk, it cannot be attributed to diskfull; it is an oversized-input/context boundary.

Final measured accounting: three completed providersteps, three fake HTTPcalls,330microcredits totalspent,0reserved. Two ordinary runs completed; one largeinput run remainspaused with its completedsummary retained. No evidence of duplicate provider calls or duplicate settlement. This does not prove every possible mid-transaction or unknown-commit failure.

## Readiness and boundaries

`/healthz` and `/readyz` remain200 during measured PostgreSQLwriteENOSPC and read-only conditions. Current readiness verifies dependency connectivity and sandbox execution, not guaranteed disk capacity/writeability. Admission success is durable202, not completion. Disk alerts and operational recovery remain necessary.

The file helper inspected the real API mount via its PID namespace; the production image remained distroless. Driver mistakes and corrected assumptions are preserved in driver-corrections.txt and driver-initial-failure.json. Product code was not modified.

Not exercised here: physical ext4 power-loss/fsync durability, PostgreSQLWAL-rotation PANIC recovery, inodeexhaustion, active-inference shutdown duringENOSPC, unknown externalservice mutation commit, hostdisk pressure or productionUID permissions. These need separately scoped faultfixtures; do not infer coverage from this report.

The owned stand is retained pending root cleanup decision. Its API is recovered, databasewritable, and the largecontext fixture remainspaused. Original OpenAI/Zitadel demos were unchanged.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
