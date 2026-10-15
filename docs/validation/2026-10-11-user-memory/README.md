# Personal memory validation — 2026-10-11

Status: implemented and locally checked. No production deployment or paid inference was performed for memory acceptance.

The implementation adds a 500-code-point micro profile and three on-demand levels: index, registry notes, detailed sources. Memory belongs to the authenticated user and organization and spans that user's chats. HTTP and script tools require authentication and access only the current user's memory; separate memory roles are not required. Micro memory is reloaded for inference and is outside cached history.

## Verified

Real PostgreSQL behavioral tests cover persistence, Unicode bounds, other-user and other-organization isolation, stale versions, concurrent updates, entry delete/recreation, atomic revision-checked clear, regex/literal search, no-match scan continuation, and partial Unicode reads.

HTTP tests cover rejection of unauthenticated requests, strict requests, OpenAPI DTOs, persistence, search/read, conflict responses, cross-user 404, and clearing. A real JS sandbox checks memory writes, reads, bounded responses, and rejection of a different user acting on the run.

A fake HTTP model verifies that micro memory updates within an active run reach the next inference, clearing removes it from a run using `previous_run_id`, and a stored micro profile is included without additional memory roles. This is request-contract acceptance; it does not test a real model's willingness to curate memory.

Before the removal of separate memory roles, two independent source review passes returned no actionable findings. The complete Windows integration suite, target Linux/Docker race suite, and Golden lint suite passed with this memory implementation. Golden reported zero issues. See [Windows log](../2026-10-11-extended-qa/final-windows-tests.txt), [Linux race log](../2026-10-11-extended-qa/final-linux-race.txt), and [Golden log](../2026-10-11-extended-qa/final-golden.txt).

A subsequent full Linux run failed in the existing sandbox health IPC test with `broken pipe`; all memory-owning packages still passed. Twenty focused Linux race repetitions of that health test passed. The original failure is preserved in [the failed gate log](../2026-10-11-extended-qa/linux-race-health-failure.txt); the defect was later reproduced and repaired in the [capacity handshake follow-up](../2026-10-11-sandbox-health-fix/README.md).

## Default access follow-up

Separate memory roles were removed at the user's request. Every authenticated user can read and edit their own memory; ownership and organization checks remain. Unauthenticated HTTP requests are rejected. Agent callbacks still check fresh authorization and the active lease, and reject a different user acting on the run.

[Windows affected-package gate](default-access-windows.txt) passed with real PostgreSQL for API, app, store, runs, and integration packages. The model fixture verifies micro-memory insertion without additional roles, within-run updates, and deletion with previous-run reuse. No paid model calls were made. A fresh independent memory/sandbox source review found no actionable findings; final full Windows/Linux race/Golden gates are in [the follow-up](../2026-10-11-fault-fixes/README.md).

## Limits

No automatic fact extraction, automatic index maintenance, embeddings, background model calls, or operator memory editor is implemented. Source links are soft references. Clearing personal memory cannot erase prior model inputs, historical tool outputs, logs, backups, or summaries that already contain the same facts. The reproduced cancellation/checkpoint and outage-shutdown findings were repaired in [the follow-up](../2026-10-11-fault-fixes/README.md); target and disk-full acceptance remain open.

Rollout requires the normal migration. Separate Zitadel memory roles are not required. See [contract and tools](../../user-memory.md).

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
