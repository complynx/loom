# Paged blob read acceptance — 2026-10-12

Script artifact pages seek directly to a validated offset instead of rereading and discarding the prefix. Owner, disclosure and cancellation checks, the 512 KiB ceiling and the existing base64/next_offset/partial contract remain unchanged. No new dependency or schema change.

Existing whole-file paging acceptance and new near-EOF, exact-EOF and invalid-offset checks passed against PostgreSQL. Full affected runtime tests on Windows, Linux runtime race tests, Golden (zero findings) and Docker build passed. One independent fresh textual-diff review returned no actionable findings; the index stayed empty throughout review. Its out-of-scope promoted-field syntax concern was rejected against the pinned Go 1.27.2 compiler, consistent with the previous feature acceptance.

[Manual receipt](manual-results.json): actual API → River → model fixture → Goja supervisor → artifact RPC → model fixture read the last 16 bytes of a 1 MiB blob. Exact bytes, next_offset=1048576 and partial=false were checked in the provider's subsequent input. Two fake inferences settled 220 microcredits with zero reserve. No paid OpenAI calls. Image identity is recorded in the receipt. This is behavioral acceptance; no throughput/latency improvement was measured.

The ignored replay is management.local/loom-night-20261012/paged-qa.ps1. Existing production and prior QA stands were unchanged. Object storage and distributed blob access remain unimplemented.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
