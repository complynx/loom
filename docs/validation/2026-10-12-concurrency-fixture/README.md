# Local concurrency fixture acceptance — 2026-10-12

Actual loopback HTTP/API/River/PostgreSQL acceptance on image sha256:4b4fc6fc48b1606ee249f2e2e2bf08c75a2d382b7543ed187c5ae21b073e7544. The application code is commit d6ab551. This is a small deterministic functional concurrency probe on Docker Desktop, not a production load envelope or throughput benchmark.

[Receipts](manual-results.json):

- Eight concurrent submissions to one chat: one 202 accepted, seven 409 chat_busy. Submission burst took 63 ms; the provider fixture deliberately delayed completion by six seconds.
- Eight independent chats for one user: two accepted, six 429 active_run_limit, consistent with the configured two-active-run ceiling. All eight completed across later two-run waves. Total measured elapsed time was 8.796 seconds, including artificial provider delays.
- Eight identical idempotency keys in a fresh chat: all returned the same run, which completed once and settled once.

The ten completed runs each spent 110 fixture microcredits and released all reserve. No paid model calls. Two earlier budget-limited synthetic runs were canceled through the public API with command IDs: spent=0, reserve=0 for both. Earlier driver errors expected 201 rather than the documented 202, used a budget below the conservative maximum-output reserve, and initially omitted cancellation Idempotency-Key. These were driver errors; they did not establish a product defect and are not represented as an initial PASS.

Post-run Docker stats snapshot: API 18.2 MiB / 512 MiB, supervisor 6.832 MiB / 128 MiB, PostgreSQL 91.3 MiB / 58.68 GiB. This is idle/post-run observation, not peak memory, OOM proof or a proposed server allocation. No fairness across multiple identities, SSE floods, upload contention, regex pressure, host OOM or sustained throughput was measured. E01/E02 remain open.

Private replay: ignored management.local/loom-night-20261012/load-qa.py. Stand project loom-night-qa-20261012. No production or old QA stand changes.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
