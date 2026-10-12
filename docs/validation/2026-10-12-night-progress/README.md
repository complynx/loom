# Loom night progress — 2026-10-12

Accepted code slices on codex/loom, pushed to complynx/loom PR #1:

- f3f773a: chat/run lock ordering repaired after the exact remote CI failure. Original failing evidence preserved; fresh final review, focused Windows checks, full Linux race/Golden and remote CI passed. The later full Windows gate for the Range slice also includes this repair. [Evidence](../2026-10-12-ci-lock-fix/README.md).
- 28a4edc: HTTP Range/conditional/HEAD delivery for artifacts and ephemeral handles. HEAD range semantics corrected after review. Focused and full Windows/Linux race/Golden/image gates, real HTTP/River fixture QA and remote CI passed. [Evidence](../2026-10-12-artifact-range/README.md).
- 9286736: direct-seek script pages. Boundary tests, affected runtime/race/Golden/image, real Goja RPC QA and independent review passed. Remote PR and push CI passed. [Evidence](../2026-10-12-paged-blob/README.md).
- d6ab551: artifact capabilities hidden without current artifacts.read, both in model catalog and JS namespace. Grant/removal behavior, runtime/race/Golden/image, two independent reviews and real supervisor QA passed. [Evidence](../2026-10-12-artifact-permissions/README.md). Latest code [PR CI](https://github.com/complynx/loom/actions/runs/38189316375) and [push CI](https://github.com/complynx/loom/actions/runs/38189314145) completed successfully.

[Small concurrency acceptance](../2026-10-12-concurrency-fixture/README.md) also passed, preserving single active run/chat, user ceiling and one settlement per idempotent request. Production envelope remains unmeasured.

No production deployment, main merge/push, paid OpenAI call or system certificate installation occurred. The new private fixture project is loom-night-qa-20261012; previous accepted stands are preserved. Dependencies and baseline migrations are unchanged by these slices. [Current CA cleanup check](../2026-10-12-ca-cleanup/README.md) separately records absent Windows root trust; the original browser acceptance report is preserved.

Pending independent work remains in the development track, including context/tool eval fixtures, dependency circuit-breaker design, runtime limits and broader resource acceptance. Installed issuer/model/service acceptance, release delivery, backup RPO/RTO/location and deployment require their existing decisions/authorizations. This report does not close those gates.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
