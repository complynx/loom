# Disk-full manual acceptance plan

Isolated project: clx-fullqa-disk-20261011. Loopback API port 58580. PostgreSQL data is a 256 MiB tmpfs; artifact root is a separate 8 MiB tmpfs. Filling either cannot exhaust host filesystem. PostgreSQL fixture data is disposable. Existing OpenAI and Zitadel stands remain unchanged. Fake HTTPS issuer/provider only; no paid model calls.

Await final review fixes before compiling the product binary and running acceptance.

1. Healthy baseline: readiness, raw upload/download SHA256, ordinary agent completion and credit settlement; sandbox actual Linux process.
2. Artifact full: upload a 10 MiB body into 8 MiB root (API and artifact limits are 20 MiB). Observe actual ENOSPC response/log, no metadata row, no .tmp/orphan content, unchanged credits. Existing baseline download remains intact. Readiness is measured rather than assumed. Small upload after failed large upload proves recovery.
3. PostgreSQL full: write an isolated filler file until dd receives ENOSPC on its bounded tmpfs. Record df and write failure. Perform write through actual HTTP service. Assert no false success, no run/provider dispatch accepted without durable admission, no duplicate credit/effect. Measure health/readiness under full disk.
4. Free filler file. PostgreSQL may stop after WAL failure: inspect actual logs, restart this owned database only if necessary. Verify persisted baseline, credits and new write/run recover.
5. Bounded shutdown while PostgreSQL cannot write: Docker TERM-to-die events must show <5 seconds; healthy shutdown target <1 second. Recovery after freeing disk records checkpoint/orphan reconciliation behavior.
6. Clean up only clx-fullqa-disk-20261011 containers/project and its socket volume after collecting sanitized evidence. Scripts and source remain private under management.local/full-qa-disk.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
