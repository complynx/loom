# Acceptance fixture behavioral QA, 2026-10-11

Result: PASS for the scoped local behaviors below. No product defect was found in this run. This is not production acceptance.

Executed against actual PostgreSQL (`clx-search-tests-20261011`, localhost 55439), a separately built `clx-acceptance` Docker fixture, real API HTTP requests, the Responses HTTP adapter with a deterministic model endpoint, the run engine, module registry, production ModuleBridge, operation coordinator and durable artifact importer. Each API fixture created and removed its own PostgreSQL schema. The existing database container was retained.

## Proven behaviors

- `go test ./internal/acceptance ./cmd/clx-acceptance -count=1 -v`: three acceptance tests PASS. The command package has no tests. These cover static/dynamic reads, permissions, input schemas, exact blob bytes/hash, confirmation rejection, duplicate preparation/execution, lost reply and process restart, and read-token write/fault denial.
- API-created run -> model HTTP function call -> actual JavaScript `modules.fixture_dynamic.lookup` -> registered Docker fixture -> production artifact import -> model continuation -> completed run: PASS. The second model request contained the read result and artifact ID. One artifact row was committed to PostgreSQL; MIME and byte length matched the fixed fixture blob. The production importer verifies the descriptor SHA-256.
- Read-token fault endpoint denial (403), registry catalog filtering without permission, and direct broker read denial without permission: PASS.
- API-created write run -> service preparation -> `waiting_approval`: PASS. A different user could not approve (404). Passive chat activity containing "approve" did not change the waiting state. Two identical explicit HTTP approval submissions returned 200.
- Approved write with intentional reply loss -> durable `reconciling`: PASS. A transparent HTTP QA proxy observed the actual lost backend reply (EOF).
- Docker container restart using the retained named `/data` volume -> explicit HTTP resume -> status recovery -> verified write result -> completed run: PASS. Proxy counts were exactly **one `/write` request and one `/status` request**. The second model request contained the committed value. The durable coordinator record was `succeeded` with the expected result; the fixture state retained that same operation ID.
- Attempting to execute an already completed run returned `run_not_runnable`; model requests remained exactly two and the write count remained one: PASS. Completed runs did not replay inference or the write.

The dedicated fixture ran as user `10001:10001`, with a read-only root filesystem, all capabilities dropped, no-new-privileges, and port 58090 bound to 127.0.0.1. State used the named QA Docker volume. Tokens were local fixture-only strings. The fixture container and its QA volume were removed after verification; the built QA image was retained for repeatability.

## Evidence and repeatability

`behavioral-results.txt` records the passing two-scenario behavioral execution (6.152 seconds). The source snapshots are `fixture_qa_test.go.txt`, `http_helpers_test.go.txt` and `model_helpers_test.go.txt`. The helper snapshots were derived from the existing integration test fixtures and modified only for the isolated QA package. They are retained as text, outside Go package discovery, because they are investigation harnesses and were not subjected to the repository Golden lint gate. No lint exclusions were added. `Run-QA.ps1` reconstructs the temporary package and cleans it after execution. Set `CLX_TEST_DATABASE_DSN` to a disposable PostgreSQL database before replay.

Initial QA-only build errors used incorrect artifact field names and an unused import; these were corrected before behavior ran. An initial assertion incorrectly required a completed-run `Engine.Execute` call to return nil. That call correctly returned `run_not_runnable`; the assertion was corrected to verify rejection and absence of additional effects. These are QA helper errors, not product failures. The final passing run includes RPC counts for the write/recovery boundary.

## Not exercised

The API authenticator and execution authorization callback are explicit test boundaries. Live Zitadel token validation, real paid provider traffic, server deployment, River scheduling and complete application process startup were not exercised. Docker metadata discovery was not exercised; the registry used an explicit trusted endpoint for the Docker fixture. JavaScript used the real `sandbox.Inline` engine and module broker through the run's script callback; the Linux supervisor IPC/confinement and private `runtimeTools` assembly were not exercised. Artifact handle download through the public API was not exercised by these added scenarios. The focused existing acceptance tests independently verify exact bytes and hash. A service restart proves ordinary restart recovery only, not host power-loss durability.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
