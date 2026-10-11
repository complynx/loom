# OpenAI, development providers and context validation

Date: 2026-10-11. Local Linux/Docker development evidence; no production rollout or paid inference.

- Full `go test -race -tags=integration ./...` passed against an isolated real PostgreSQL 17 container. See linux-race.txt.
- Initial focused golangci-lint 2.14.0: zero issues. The later full Golden cleanup, final Linux race run and container build are recorded at ../2026-10-11-golden-lint/README.md.
- Linux/amd64 production container build passed. See docker-build.txt.
- Behavior coverage: catalog default/explicit override; separate model input ceiling; short/long tariffs; disjoint cache reads/writes; exact once-only ledger settlement; manual QA provider completion and malformed-tool charging; Codex subprocess structured output, measured usage and cancellation; passive-action compression across consecutive runs; 1005-action pagination; single summary for a 100-action block with an interleaved fact.
- Mock test drives the actual run engine and PostgreSQL ledger. QA usage is explicit scenario data. Codex tests use a fake executable, not a live model/account.
- Current default hard input limit: 272000. Compaction threshold: 244800. Target: 204000. Values remain model-configurable; the process input ceiling also applies.
- Existing database settings are not overwritten by bootstrap. Operators must use authorized settings changes for an existing installation.
- Historical context still uses conservative byte bounds; active-run item compaction and a measured token-aware retained-tail policy remain future work.
- CI publishing and receiver dispatch were configured and separately reviewed; no Hub publication or server deployment was performed. The server_configs clx-scripter deployment profile remains a prerequisite.
- Zitadel live QA has its separate report at ../2026-10-11-zitadel/README.md: 27 checks passed; exchange-token revocation and refresh issuance remain unproven/provider gates.

Review loop: independent textual-diff reviews; source-backed fixes for historical-gap rehydration, activity-placeholder checkpoint coverage, development image capabilities and known billed usage on malformed model output. Index stayed empty; no staging or commit.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk