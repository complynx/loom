# CI lock-order repair — 2026-10-12

The failed [GitHub Actions job](https://github.com/complynx/loom/actions/runs/38183430207) ran on branch codex/loom, commit ad013866. It was a push workflow, not a pull-request workflow; the repository had no PR at investigation time.

`TestConcurrentFinalizationAndDeletionPreservesSettlement` returned database_busy instead of lease_lost. PostgreSQL logs showed both run-row and chat-sequence lock timeouts. DeleteChat locked chat then run; finalization locked run then appended chat history, creating a cycle. The [CI excerpt](ci-failure-excerpt.txt) retains the actual failure.

History-writing transaction entry points now lock the immutable parent chat before the run. Completion, question, approval and input writes share this boundary; internal provider-retry transitions that do not append chat history remain unchanged. Ownership, lease fencing, paid-step settlement and reservations still use their existing checks.

The new PostgreSQL regression holds the run row, observes finalization waiting, and proves deletion waits behind finalization on the chat. Both then finish; the deleted run is hidden and the paid 3 microcredits remain settled with zero reserve. It [fails on the original source](original-code-regression.txt) and passes after repair. Three repeated focused runs passed. Earlier weak test coordination was corrected after independent review.

Three fresh review passes: a test declaration issue and a regression coverage gap were fixed; final pass had no actionable findings. No linter rules were weakened. Golden passed with zero issues. Complete Linux/Docker race integration passed using the exact CI PostgreSQL image. Final source has only additional formatting and test-strengthening fixes after the initial local gate; the final full Linux gate also passed. Windows focused checks passed. Live models and production were not used.

Git staging remained empty during review. The fix is committed/pushed only after these local gates; the remote workflow result is checked separately. The previous paid-model/browser/runtime/fault evidence remains unchanged.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
