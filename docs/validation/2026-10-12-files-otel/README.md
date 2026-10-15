# File tools, live limits and OpenTelemetry acceptance

Date: 2026-10-12. Platform: local Windows and Docker Linux/amd64. Production was not changed. No paid OpenAI request was sent.

## Implemented and checked

- Private Unix-socket file worker streams input/output outside JS. PDF extraction/search/rendering, image resize/crop, video/GIF frames/storyboards/audio, bounded text/search/CSV/JSON/XML/HTML, archives and basic office/EPUB views have behavioral fixtures. Real pinned Poppler and FFmpeg run in the Linux gate. Oversized/unsafe inputs, UTF-8 pagination, JSON escaping and full selected-slide PPTX continuation are covered.
- Derived artifacts keep ownership, organization, source provenance/disclosure and quota. PG/API/Goja fixtures prove handle use, partial reads, image output and access denial.
- OpenAI transcription uses a local HTTP fake only. Actual WAV extraction precedes a durable paid step. Frozen duration tariff, exact fractional-duration settlement, replay without redispatch, unknown holds, manual receipt reconciliation, live permission revocation and original-source deletion are checked. Default model is gpt-transcribe at 75 microcredits/second; real provider acceptance remains pending.
- Global admission is serialized with settings updates. Concurrent admission, legacy zeros, deployment ceilings and next-script/next-model reductions pass. Lowering does not interrupt in-flight operations; increasing cannot grow admitted snapshots.
- OTel fake-collector tests cover correct /v1/traces and /v1/metrics paths, scoped providers, parent propagation, log correlation, header cloning, privacy, collector failures/partial rejections and bounded shutdown. Failures use fixed safe warnings and scrape counters.
- Real local Alloy v1.20.1 accepted two spans and two operation metrics from a synthetic HTTP/model-span fixture. Raw path identifier/query were absent; parent linkage and log trace IDs were verified. No model request was made. The prepared stable rollout snippet passed Alloy validation. The disposable QA debug exporter needed the experimental stability flag; the production snippet does not use that exporter. The final explicit histogram bounds passed the subsequent test/build gates; the real Alloy fixture preceded that bucket adjustment.
- A worker forced SIGKILL exited 137. Explicit restart on the same persistent socket volume restored health 204 and paged text processing. Docker kill suppressed automatic restart, so this proves forced-kill plus explicit restart, not automatic restart scheduling.

## Review and initial failures

Fresh independent source reviews covered correctness/security, code quality, paid settlement/replay, documentation/CI and OTel. Verified defects fixed included tool admission, reconciled function output, UTF-8 cursors, rich XLSX text, stale sockets, escaped JSON limits, source authorization before upload/replay, OTLP paths, unsafe SDK exporter error logging, Docker notices and PDF/PPTX continuation. No fixes were staged during the loop. Final review outcome and final gate results are recorded below after completion.

Initial govulncheck identified reachable vulnerabilities in the previous x/net version. The graph was upgraded to x/net v0.61.0 and required companion versions. Initial final Docker build failed because the new notices were stored one directory above the COPY target; the exact 72 notice files were moved under notices before rebuilding. These failures were corrected rather than treated as passes.

## Dependencies and delivery

OTel uses the official Go SDK v1.47.0. All 231 graph modules have full VCS commit pins. New/upgraded module notices are archived under docs/dependency-audit/2026-10-12-otel/notices and included in both containers. The worker includes matching corresponding-source material for isolated GPL CLI dependencies. Its image is approximately 1.47 GB; source collection is an intentional build/runtime-image cost.

## Not accepted or not implemented

- Production receiver/network/backend, Grafana delivery and alerts; target-server resource/load envelope; production backup/restore.
- Real paid OpenAI transcription, live vision and external provider quality/cache measurements.
- OCR, PDF table reconstruction, office layout rendering/formula execution, video editing, realtime transcription, subtitle translation and archive-wide extraction.
- EXIF orientation adjustment, EPUB spine ordering, automatic transcript unknown-dispatch retry or token-billed transcription admission.
- Durable trace continuation across queue restarts. OTel is best-effort telemetry, not a durable audit trail.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
## Final gates

- Full Windows `go test -parallel=4 -tags=integration ./...`: PASS.
- Full Linux `go test -race -parallel=4 -tags=integration ./...` with PG and real pinned Poppler/FFmpeg: PASS.
- Golden `golangci-lint run --build-tags=integration ./...`: zero issues.
- `go mod verify`: all modules verified.
- Pinned Linux `govulncheck ./...`: No vulnerabilities found.
- Docker API and file-worker builds: PASS. Worker source bundle: all 757 SHA-256 checks PASS; 72 new dependency notice files present.
- Local image sizes: API 51,036,043 bytes; worker 1,475,494,590 bytes. Image IDs are local build evidence, not published release digests.
- `git diff --check`: PASS; review fixes remain unstaged until final source review completes.
- Dependency lock check: all 231 full commit pins verified.
- License manifest: 69 modules match locked versions/commits; all 72 notices byte-match the exact Go module cache.

- Final general source review (pass 6): No actionable findings. Final dedicated OTel review (pass 2): No actionable findings. Earlier verified findings were fixed and retested. Staged diff stayed empty throughout review.

## GitHub source mirror repair

Initial push and PR CI for 3fde8d1 passed dependency checks, both race suites, Golden and govulncheck, then failed in source-bundled worker construction. Alpine GitLab returned HTTP 418 for apk-tools-v3.0.8.tar.gz. CI run: https://github.com/complynx/loom/actions/runs/38275172599 .

The source collector now uses abuild's native DISTFILES_MIRROR with the official Alpine v3.24 distfiles service first, retaining upstream fallback and exact pinned APKBUILD checksum verification. A clean Alpine build environment downloaded the formerly failing archive and verified its checksum and patch. Deliberate archive corruption was rejected. A fresh independent source review of this repair reported No actionable findings. No source or license requirement was removed. The production worker still runs as UID 10001; the acceptance fixture uses build-stage tooling.

Linux Golden with integration tags also passed, including Linux-only socket ownership/recovery code.
Mirror repair full worker rebuild: PASS; all 757 source checksums PASS. GitHub rerun is tracked separately from this local result.

## Readiness fixture follow-up

PR CI for c708402 passed the main race suite but failed TestWorkerReclaimsStaleSocket in the media suite: the fixture did not observe health within one second. The same push media suite passed. Before the patch, 25 repeated Linux race runs of the socket tests passed locally; scheduling is a plausible cause, not a proven runtime diagnosis.

The test-only startup wait now allows five seconds, polls every 10 ms and fails immediately with the actual worker exit error. Production readiness and one-second shutdown assertions are unchanged. Thirty complete fileproc race repetitions with real media CLIs passed after the patch. Scoped Linux Golden reports zero issues. Fresh independent textual review found No actionable findings; the staged diff stayed empty during review.