# Conversational input acceptance, 16 October 2026

Scope: working changes on `codex/conversational-steering`, base `d9a61ac`.
No production deployment, paid provider call, or Zinusya adapter change.

## Verified locally

- Full Linux Go 1.27.2/PostgreSQL 17 suite: `go test -race -parallel=4 -tags=integration -count=1 ./...` PASS.
- `go mod verify`: all modules verified. Golden `golangci-lint run --build-tags=integration ./...`: 0 issues.
- HTTP steering wakes the same run. Two clarifications retain one blocked action; consent or refusal then resolves it. A new engine continues its persisted checkpoints.
- Qualified consent rejects the old operation; corrected parameters get a new preparation and require a new message. Only that operation dispatches.
- Critical native callbacks work after conversational clarification without agent interpretation. Without callbacks, the model's localized phrase is compared exactly; any other message rejects the action. No codes are added.
- Runtime rejects missing/wrong input, stale revision, wrong binding/action, conflicting presentation, expired approval, protocol action through the agent resolver, and wrong execution lease.
- A queued-input failure rolls back acceptance. Wait publication does not strand steering accepted during inference. Retries return the original input receipt after later messages in both interaction modes.
- Under the dispatch lock, an unapplied correction or newer conversation revision prevents old consent from dispatching.
- Existing authorization, mutation idempotency, unknown-outcome recovery, media and runtime tests also passed in the full suite.

Independent review found two defects (conflicting exact-text presentation references and retry receipts). Both were corrected and covered by behavioral tests. A fresh review found no actionable issues. The staging index remained unchanged.

The owned QA containers are temporary; the sibling Shuttle stand is outside this acceptance scope.

## Not verified

Scripted provider peers prove transport and deterministic effect gates, not language understanding. Russian/English real-model interpretation and Zinusya ordered delivery, keyboard retirement and mixed UI behavior still require acceptance. With no reference/code, identical delayed text cannot be distinguished from fresh text; the client must serialize pending actions and delivery as documented in [the contract](../../conversational-input.md).
