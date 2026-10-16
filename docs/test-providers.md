# Development providers

Use these opt-in transports to test harness behavior through startup YAML. Identity, permissions, approvals, disclosure checks, run snapshots and the credit ledger still apply. Use a separate test database with test credits: responses spend credits at the configured model tariff. These transports do not add a public QA endpoint.

To use the automatic OpenAI bootstrap catalog, replace only the `providers.openai` block. Keep the alias `openai` and omit a custom catalog and tariffs. This does not replace settings already stored in PostgreSQL. To compare transports in an existing test database, keep the model ID and tariff unchanged.

## Manual QA mock

```yaml
providers:
  openai:
    kind: mock
    mailbox: /absolute/path/to/qa-mailbox
```

Create a private directory owned by the harness/QA operator before startup. Do not expose it through a web server or commit its contents. Automatic development presets are text-only. Artifact token counting is not implemented for these transports.

Each inference step publishes a complete `<sha256(ClientRequestID)>.request.json`. The file contains the provider Request, including Model, Effort, Instructions, Input, Tools and MaxOutputTokens. QA reads it and acts as the model, including selecting available tools and following their schemas.

Write a temporary response in the same directory, then atomically rename it to the matching `.response.json` name. Example:

```json
{
  "ID": "qa-response-001",
  "Text": "Andrey matches your preferences.",
  "Calls": [],
  "Usage": {
    "InputTokens": 1000,
    "OutputTokens": 20,
    "CachedInputTokens": 0,
    "CacheWriteTokens": 0
  }
}
```

Usage is required and entered by QA; these are scenario token counts, not measured model usage. OutputTokens includes reasoning when the scenario models it. Counts must be nonnegative; cache reads and writes cannot overlap or exceed total input. To call a tool, include `{"ID":"unique-call-id","Name":"name-from-Tools","Arguments":{}}` in Calls and supply arguments according to its schema. A question is a normal harness tool call. The broker applies the same permissions and approval rules as for OpenAI. Omit Output; the adapter constructs canonical model items from Text and Calls. Refused and ErrorCode are optional provider fields.

Answer before the harness inference deadline, including summary requests when history needs compaction. Requests are idempotent: an existing request ID with different contents is rejected. Files stay available for inspection and explicit operator cleanup. Unknown usage follows the normal [provider recovery policy](active-compaction.md): eligible sponsored failures release the reservation; other uncertain outcomes require reconciliation. Do not overwrite responses after completion.

## Local Codex

```yaml
providers:
  openai:
    kind: codex
    executable: /absolute/path/to/codex
    codex_home: /absolute/path/to/dedicated-codex-home
```

Replace these paths with existing absolute paths. On Windows, use a path to codex.exe and a dedicated profile directory. Install Codex and log in with that dedicated `CODEX_HOME` as the operator; the adapter neither copies personal credentials nor accepts them through the HTTP API. The normal production image does not install Codex: use a local development runtime or provision the binary separately. The required exec flags and event schema are defined in `internal/provider/development_codex.go`; check compatibility with the CLI version you install.

Each step launches an ephemeral exec session in an empty temporary directory, ignores user configuration and repository rules, disables tool features, requests read-only sandboxing and a JSON output schema. The harness request is passed through stdin. Codex returns text or requested harness calls as structured JSON. The harness executes those calls; Codex receives the results on the next step. Unexpected built-in tool events fail closed. Feature flags and event checks do not constitute a separate OS isolation boundary; run this development adapter under a restricted dedicated account/container. It is a Codex agent invocation, not a raw Responses request, so CLI system instructions and formatting can affect prompt-tuning results.

The adapter currently supports text input only. It rejects images, files and audio rather than dropping them. It does not resume Codex sessions: the harness supplies its own conversation state. Credentials remain in the dedicated profile. Child diagnostics are bounded and never returned verbatim to API clients or logs.

Usage comes from actual `turn.completed` events, including cached input and output. The model cannot choose its usage. Missing or invalid usage and interrupted execution follow the same provider recovery policy as other uncertain outcomes. CLI usage includes its own prompt/schema overhead; production tariff arithmetic remains the same, but this is a simulated API cost, not the price charged by a Codex subscription. CLI output limits are enforced on returned usage; the CLI can consume more than the requested harness output limit before it is rejected, so keep the adapter restricted to test accounts.

See [context policy](context-research.md) for harness input limits and [OpenAI catalog and context](openai.md) for the tariff snapshot. Before using an installed Codex CLI, verify a text reply, a harness tool call, usage reporting and interruption recovery in a separate test database. Fixture coverage in `internal/provider/development_test.go` and `integration/development_provider_test.go` does not establish compatibility with that CLI.
