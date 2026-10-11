# Active-run compaction and sponsored recovery validation

Date: 2026-10-11. Local working tree; no production deployment or paid model request.

## Passed

- Full PostgreSQL integration suite and full Linux race suite passed during implementation.
- After review fixes, `go test -parallel=4 -tags=integration ./internal/provider ./internal/store ./integration` passed with isolated PostgreSQL on localhost:55439.
- The same three packages passed with `-race` in the pinned Go 1.27.2 Linux image, using host.docker.internal:55439.
- `golangci-lint run --build-tags=integration ./...`: 0 issues. `git diff --check` passed. Staged diff is empty.
- Behavioral cases cover native/fallback compaction, completed call/result boundaries, question/resume, retained history, opaque token counting, successful usage settlement, lost model/compact replies, retry cap, expired leases and revoked authorization.
- Review fixes add canonical retained reasoning/tool items without redispatch, and transaction-protected client pause/cancel that retains the user's unresolved reserve instead of sponsoring it.

## Independent review

Two fresh textual-diff passes. First pass found canonical-window validation and a client-interruption race; both were corrected and covered by focused tests. Second pass: no actionable findings. Reviewers executed no tests. No files were staged.

## Open

No live OpenAI compact request, quality evaluation, or paid cost/cache measurement. Fixture output is not evidence of model quality. No global platform exposure ceiling, late receipt settlement, or general dependency circuit breaker/Retry-After policy.

Full-output reservation is conservative because the compact endpoint has no output cap. Possible platform exposure is not actual provider billing.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
