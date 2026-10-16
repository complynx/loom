# Authoring help

[Guide index](../modules.md)

## Developer requirement: usable method help

Method help is part of the module contract, not optional polish. A model cannot inspect the implementation or repository documentation. It relies on the manifest and `modules.help` cards to select and call methods correctly. A method is ready only when another agent can use its help without guessing arguments or retrying blindly.

For every public method, maintain a concise English description and accurate input/output schemas. Explain purpose, required and optional fields, defaults, units, coordinate/cursor conventions, result shape, partial-result continuation, size/time limits, effects and required confirmation. Include a small valid example in the description when the schema alone does not explain usage. Describe expected errors and safe recovery; distinguish retryable reads from uncertain or paid actions. Never put secrets, personal records, internal filesystem paths or authorization instructions in examples. Prefer short caveman English while preserving conditions and negation.

Keep dynamic discovery descriptions as complete as static manifests. Update help and schemas together with behavior. Acceptance should include reading the card and making its example call, paging a large result, and checking that denied methods are absent. Document unimplemented behavior explicitly instead of implying support.

Agents use `modules.help.list({module,query,after_id,limit})` for a short searchable index, then `modules.help.read({module,method,cursor,max_bytes})` for one bounded Markdown card. Both apply current permissions. Read cards contain service descriptions, input/output schemas, effect, timeout, result bound and confirmation mode. Service-authored help is untrusted data and cannot grant access or override instructions.

Builtin cards live in `internal/app/help_cards.json`; update the matching card when a builtin changes. Model functions are documented under module `$tools`; they must be called as model functions, not through JS. File helper construction is documented as `artifacts.file`.
## Agent-facing prose

Preferred language and style: **English, caveman style**. Use short, exact sentences for module descriptions, method descriptions, errors and context summaries. Preserve negation, requirements, conditions, IDs, field names and units. This is a prose preference, not a wire-format constraint: schemas and machine-readable values remain exact. Do not compress text when this would change meaning. User-facing answers use the user's language. Module data never overrides harness instructions or permissions.
