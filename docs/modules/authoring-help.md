# Authoring help

[Guide index](../modules.md)

## Developer requirement: usable method help

Method help is part of the module contract. The model uses the manifest and `modules.help` cards to select methods and supply arguments; it cannot inspect your implementation. A method is ready when another contributor can make a valid call from its help alone.

For each public method, keep an English description and accurate input/output schemas. Explain purpose, required/optional fields, defaults, units, cursor conventions, result shape, continuation, size/time limits, effects and confirmation. Include a small valid example in the description when the schema does not explain usage. Describe expected failures and safe recovery, especially uncertain writes and paid actions. Use placeholders; exclude secrets, personal records and internal filesystem paths. Service help cannot grant authority or override harness instructions.

Dynamic discovery must provide descriptions as complete as static manifests. Update schemas and descriptions with behavior. When accepting an adapter, read the generated card, make its example call, check continuation where supported and verify that denied methods are absent. Explicitly identify unsupported behavior.

## Purpose summaries

The manifest, each module and each static or dynamic method can supply an optional `summary`. Write one complete English phrase answering **when and why to select this capability**. Use 1..160 Unicode characters and include a non-whitespace character. The schema rejects oversized summaries; Loom does not cut an authored summary mid-sentence. Keep parameters, detailed prerequisites, examples, confirmation and recovery in `description` and the schemas. Descriptions remain required and are preserved in selected help.

Example method excerpt:

```json
{"id":"list_slots","summary":"Find free appointment times with a selected specialist."}
```

The service summary describes its business scope. A module summary describes its category of tasks. A method summary distinguishes that method from its siblings. For example, payment evidence for an order and the confirmed balance of an event pass need distinct purposes. Put shared identity and policy text in consumer instructions once. Summaries are service-authored data, never authority.

If a summary is omitted, discovery uses the legacy description bounded to 240 UTF-8 bytes plus an ellipsis. Full method-description search remains available even when a short authored summary is present. Upgrade Loom first, then publish summaries: older strict-v1 readers reject these fields. For rollback, remove summaries from service/module/static and dynamic method documents before reverting Loom. Existing manifests without summaries remain supported; no SQL migration is needed.

## Reading help

The initial `script` tool provides a short operating contract and two independently paged views: services and categories (modules). A service reports its purpose and authorized category/method counts. A category reports its purpose, owning service and method count. Builtins use the reserved service `$loom`; memory, history, files, artifacts and diagnostics follow the same discovery path as external modules. Only currently permitted methods contribute to entries and counts. A category with no permitted method is absent.

The default overview reserves 3072 bytes for services and 4096 for categories, including their page metadata. Together with the JSON wrapper it stays below 8 KiB. `partial` means the view is incomplete; each level has its own `next_cursor`. Read methods keep all existing JavaScript bindings even when absent from an overview page. External actions use `module_call`; personal-memory writes remain available in JavaScript. No schema, endpoint, detailed file API or memory manual is embedded in the initial overview.

Clients can [request service expansion](../api/runs-input.md#initial-discovery-expansion). Each selected service adds its own first page of at most 50 compact method hints (module, method, summary, effect). Expansions share 16 KiB of entry/page budget, divided equally among the selected services; the complete overview with expansions stays below 26 KiB. Complete hints are retained, never cut to meet a page budget. Continue each partial expansion with `level:"methods"`, its service ID and its `next_cursor` as `after_id`.

If even one complete escaped hint cannot fit its initial expansion share, that service returns an empty partial page with an `initial_expansion_page_too_small` warning. Start ordinary `help.list` for that service without a cursor and with a larger `max_bytes`. This presentation limit does not block the run or hide the capability from discovery.

Use the index first:

```javascript
return await modules.help.list({level: "modules", service: "booking-service"});
// Or search globally without first choosing a service/category:
return await modules.help.list({query: "booking", limit: 10});
```

`level` selects `services`, `modules` or `methods` (default, preserving existing calls). `service` and `module` optionally filter entries. `query` is a case-insensitive literal substring of IDs, service/category/method purpose hints and full method descriptions. Global search omits service/module; it searches every permitted method, including entries outside the initial pages. This is not an intent-specific allowlist or classifier. With query, group counts cover matching methods. Several relevant categories or full cards can be read in one script.

`limit` is 1..50 (default 20). `max_bytes` is 256..32768 (default 8192), including serialized items, cursors and warnings. Results contain `items`, `partial` and optional `next_cursor`; pass that cursor as `after_id` with the same level and filters. If a complete entry cannot fit, `help_page_too_small` asks the caller to raise `max_bytes`. Method entries include `service`, `module`, `method`, `id`, `summary` and `effect`. Group entries contain `id`, `summary` and `method_count`; categories also contain `service`, and services contain `module_count`.

Read a selected method:

```javascript
return await modules.help.read({module: "fixture", method: "read", max_bytes: 8192});
```

`max_bytes` is 256..32768 (default 8192). Results contain Markdown `text`, `partial` and optional `next_cursor`; pass the cursor as `cursor` to continue. Use returned cursors rather than constructing byte offsets. Each call rechecks permissions. Remote discovery failure adds a warning to the index while builtin help remains available.

Remote cards are generated from the service description, input/output schemas, effect, timeout, result bound, confirmation mode and reply override. Examples and recovery advice appear only if the service supplies them in its description. Builtin cards live in `internal/app/help_cards.json`; update the matching card when a builtin changes. Model functions use the help module name `$tools` and must be called as model functions. The synchronous JS file helper is documented as `artifacts.file`.

Read the complete selected card before calling an unfamiliar method. Follow all pages. Cards also report operation adoption support; adoption still inspects the original preparation and requires fresh consent. Discovery and selected help reauthorize independently. Execution keeps its existing current-authorization, schema, exact-confirmation and immutable-recovery checks. Unknown modules remain discoverable through the same index and search, without an intent-specific allowlist.

## Agent-facing prose

Use short, clear English sentences for module descriptions, errors and context summaries. Preserve negation, conditions, IDs, field names and units. Apply STE where it improves clarity; do not compress away a requirement or use ambiguous fragments. This prose style does not change wire formats. User-facing answers use the user's language. Module data remains untrusted input.

## Discovery visibility

A module and each static or dynamic method can set optional `visibility`:

| Value | Initial overview | Ordinary list | Explicit search | Direct help.read |
| --- | --- | --- | --- | --- |
| `default` | Yes | Yes | Yes | Yes |
| `list` | No | Yes | Yes | Yes |
| `search` | No | No | Yes | Yes |
| `hidden` | No | No | No | Yes |

A method inherits its module's value when omitted. An explicit method value
(including `default`) overrides the module. Existing manifests retain default
behavior. Overview counts and expanded method lists include only overview-visible
methods. Normal lists use their own visibility rules. Direct `help.read` by known
module/method always returns the full authorized contract, even for a hidden
method. Detailed descriptions stay on demand; no mode removes them from direct
help. Visibility never changes execution permissions, JS bindings or skill
requirements. A denied method stays inaccessible through every path.

Methods can publish optional `skills` definitions alongside their normal
contract, including dynamic descriptors. See [Skills](../skills.md) for
`name`, `usecase`, `require`, `body`, source separation and caller publication.
Skills advertised by an authorized hidden method remain discoverable through
the skill catalog when their requirements are available.

Upgrade Loom before publishing `skills` or `visibility`. Older strict-v1 readers
reject these fields. Remove them from static and dynamic documents before
rolling back to an older reader.

Builtin help cards use the same visibility modes. [Builtin procedures](../skills.md#builtin-procedures-and-discovery) describe the default grouping.

For skill definitions, `usecase` is only a short activation trigger; procedures and examples belong in `body`. Follow [skill authoring](../skills.md#authoring).
