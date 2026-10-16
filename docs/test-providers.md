# Development providers

These transports are opt-in through startup YAML. They do not add a public QA endpoint and do not change identity, permissions, approvals, disclosure checks, run snapshots or the credit ledger. Use a separate test database with test credits: QA responses still spend credits at the model's production tariff.

To keep automatic OpenAI catalog and prices, replace only the `providers.openai` block. Keep the provider alias, catalog model ID and tariff unchanged.

## Manual QA mock

```yaml
providers:
  openai:
    kind: mock
    mailbox: /var/lib/clx-qa/mailbox
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

Answer before the inference deadline (currently two minutes), including summary requests when history needs compaction. Requests are idempotent: an existing request ID with different contents is rejected. Files stay available for inspection and explicit operator cleanup. A cancelled/dispatched request with unknown usage retains its credit reservation for reconciliation. Do not overwrite responses after completion.

## Local Codex

```yaml
providers:
  openai:
    kind: codex
    executable: /usr/local/bin/codex
    codex_home: /var/lib/clx-qa/codex-home
```

On Windows use absolute paths to codex.exe and a dedicated profile directory. Install/login to Codex in that dedicated CODEX_HOME as the operator; the adapter neither copies personal credentials nor accepts them through the HTTP API. The normal production image does not install Codex: use a local development runtime or provision the binary separately. This adapter targets the installed 0.157.1 exec interface; validate compatibility after CLI updates.

Each step launches an ephemeral exec session in an empty temporary directory, ignores user configuration and repository rules, disables tool features, requests read-only sandboxing and a JSON output schema. The harness request is passed through stdin. Codex returns text or requested harness calls as structured JSON. The harness executes those calls; Codex receives the results on the next step. Unexpected built-in tool events fail closed. Feature flags and event checks do not constitute a separate OS isolation boundary; run this development adapter under a restricted dedicated account/container. It is a Codex agent invocation, not a raw Responses request, so CLI system instructions and formatting can affect prompt-tuning results.

The adapter currently supports text input only. It rejects images, files and audio rather than dropping them. It does not resume Codex sessions: the harness supplies its own conversation state. Credentials remain in the dedicated profile. Child diagnostics are bounded and never returned verbatim to API clients or logs.

Usage comes from actual `turn.completed` events, including cached input and output. The model cannot choose its usage. Missing/invalid usage and interrupted execution preserve reservations. CLI usage includes its own prompt/schema overhead; production tariff arithmetic remains the same, but this is a simulated API cost, not the price charged by a Codex subscription. CLI output limits are enforced on returned usage; the CLI can consume more than the requested harness output limit before it is rejected, so keep the adapter restricted to test accounts.

No live Codex inference was used for adapter verification. Tests use a subprocess fixture plus a real PostgreSQL engine/ledger test for the manual provider. See [context research](context-research.md) for the current Codex context parameters and [OpenAI pricing](openai.md) for the production tariff snapshot.

Written by Codex (gpt-6/Codex desktop)
on behalf of Daniel Drizhuk
