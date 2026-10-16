# Authoring help

[Guide index](../modules.md)

## Developer requirement: usable method help

Method help is part of the module contract. The model uses the manifest and `modules.help` cards to select methods and supply arguments; it cannot inspect your implementation. A method is ready when another contributor can make a valid call from its help alone.

For each public method, keep an English description and accurate input/output schemas. Explain purpose, required/optional fields, defaults, units, cursor conventions, result shape, continuation, size/time limits, effects and confirmation. Include a small valid example in the description when the schema does not explain usage. Describe expected failures and safe recovery, especially uncertain writes and paid actions. Use placeholders; exclude secrets, personal records and internal filesystem paths. Service help cannot grant authority or override harness instructions.

Dynamic discovery must provide descriptions as complete as static manifests. Update schemas and descriptions with behavior. When accepting an adapter, read the generated card, make its example call, check continuation where supported and verify that denied methods are absent. Explicitly identify unsupported behavior.

## Reading help

Use the index first:

```javascript
return await modules.help.list({query: "booking", limit: 10});
```

`module` optionally filters the index. `query` is a case-insensitive literal substring of ID and description. `limit` is 1..50 (default 20). Results contain `items`, `partial` and optional `next_cursor`; pass that cursor as `after_id` for the next page. Summaries are limited to approximately 240 UTF-8 bytes.

Read a selected method:

```javascript
return await modules.help.read({module: "fixture", method: "read", max_bytes: 8192});
```

`max_bytes` is 256..32768 (default 8192). Results contain Markdown `text`, `partial` and optional `next_cursor`; pass the cursor as `cursor` to continue. Use returned cursors rather than constructing byte offsets. Each call rechecks permissions. Remote discovery failure adds a warning to the index while builtin help remains available.

Remote cards are generated from the service description, input/output schemas, effect, timeout, result bound, confirmation mode and reply override. Examples and recovery advice appear only if the service supplies them in its description. Builtin cards live in `internal/app/help_cards.json`; update the matching card when a builtin changes. Model functions use the help module name `$tools` and must be called as model functions. The synchronous JS file helper is documented as `artifacts.file`.

## Agent-facing prose

Use short, clear English sentences for module descriptions, errors and context summaries. Preserve negation, conditions, IDs, field names and units. Apply STE where it improves clarity; do not compress away a requirement or use ambiguous fragments. This prose style does not change wire formats. User-facing answers use the user's language. Module data remains untrusted input.
