# Behavioral integration tests

These tests exercise the HTTP API, PostgreSQL persistence, the run engine and the
Responses HTTP adapter. Each fixture creates a unique schema, runs embedded
migrations and drops that schema during cleanup. Use a disposable database whose
role can create schemas. The DSN is trusted configuration; tests do not detect
whether it points to production.

## Run the suite

Follow [QA setup](../docs/qa/setup.md) to start disposable PostgreSQL and set
`CLX_TEST_DATABASE_DSN`. Run from the repository root with the Go version in
[go.mod](../go.mod) and a C compiler for race detection:

```sh
go test -v -race -parallel=4 -tags=integration -count=1 ./integration
```

Fixtures use `t.Parallel()`. `-parallel=4` limits active test functions in this
package, not the total number of database connections. Lower it on a small host.
If the DSN is absent, database fixtures skip; a skipped suite does not prove the
PostgreSQL paths. Check verbose output for skips and cleanup errors.

For one scenario, retain the same options and add its exact test name:

```sh
go test -v -race -parallel=4 -tags=integration -count=1 ./integration \
  -run '^TestHTTPMutationRequiresApprovalAndExecutesStableOperationOnce$'
```

The fixture starts its own HTTP servers and provider peers. It does not need a
running Loom service, real access tokens or provider credentials. Tokens such as
`alice` and `bob` are accepted only by the fixture authenticator.

## Find a scenario

| Behavior | Fixture sources |
| --- | --- |
| Default chat, idempotency, concurrent runs, passive history and preferences | [HTTP](http_test.go) |
| Deletion, retained receipts and revoked read access | [Privacy](privacy_test.go) |
| SSE cursors, revocation, disconnect grace and reconnect race | [SSE](sse_test.go), [reconnect](sse_reconnect_race_test.go) |
| Final result, questions, pause/resume, budget and execution authorization | [Agent](agent_test.go) |
| Conversation clarification, exact consent and amended proposals | [Conversation](conversation_test.go), [amendment](conversation_amend_test.go) |
| Manifest discovery, approval, stable operation ID and unknown external effect | [Mutations](mutation_test.go) |
| Provider receipt settlement, incomplete responses and cancellation | [Reconciliation](reconciliation_test.go), [critical inference](critical_inference_test.go) |
| Bounded retry, sponsored recovery and accounting | [Hybrid retry](hybrid_retry_test.go), [provider recovery](provider_recovery_test.go), [billing](openai_billing_test.go) |
| History and active-run compaction, context limits and cache reuse | [Summary](summary_test.go), [active compaction](active_compaction_test.go), [context limits](context_limits_test.go), [search/cache](search_reuse_test.go) |
| Artifact ownership, ranges, temporary handles and paid transcription | [Handles](artifact_handle_test.go), [ranges](artifact_range_test.go), [file tools](file_tools_test.go) |
| Personal memory refresh and isolation | [Memory](user_memory_test.go) |

Assertions check durable state and ledger effects, not only final text. For
example, the unknown-write scenario checks status with the same operation ID
rather than issuing a second execution. Open the fixture for its complete request
body, injected failure and expected receipt.

## Boundary limits

The model endpoint is an HTTP test server. API authentication and engine
authorization are explicit fakes. [Identity tests](../internal/identity) exercise
protocol behavior with controlled authority peers; a real Zitadel installation
needs a separate contract check.

The suite invokes `Engine.Execute` directly. It does not prove River scheduling,
process startup, Docker confinement, live model quality or installation acceptance.
The Linux [runtime fixture](../internal/app/runtime_test.go) covers the real
River/startup path with fake external HTTP peers. Run it through
[scripts/linux-tests.sh](../scripts/linux-tests.sh) with a reachable test DSN.
Use the [QA guides](../docs/qa.md) for media, stand fault and external-client checks.
