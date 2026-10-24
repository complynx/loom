# Output validation

[Run creation](runs-input.md#create-a-run)

## Consumer contract

Before enabling this policy, request authenticated `GET /v1/capabilities` with
`chat.read`. Require `output_validation.version` equal to `1` and
`output_validation.disallow_custom_emoji` equal to `true`. Missing capability or
an unsupported response means the consumer must not assume enforcement.

Create the run with:

```json
{"input":{"text":"Reply in Russian"},"output_policy":{"disallow_custom_emoji":true}}
```

The policy persists in the run document and participates in creation idempotency.
The same key with a changed policy conflicts. Omission preserves default clients,
including retries created before this contract. No database migration is needed.
Upgrade every executor before enabling the policy; older executors cannot enforce
it. Drain restricted runs before binary rollback.

## Validation and correction

The gate rejects Telegram HTML `tg-emoji` elements and Markdown/image-Markdown
links or HTML anchors targeting `tg://emoji`. Ordinary Unicode emoji, discussion
of custom emoji, escaped HTML examples and ordinary links remain allowed.
Structured custom-emoji entities are not an accepted output field: questions
and choices accept text/labels only; final results are strings. This policy does
not add a rich-entity protocol or render arbitrary JSON as Telegram entities.
Reaction capability remains separately configured by `interaction.reactions`.

Checks run before final text, question text/choice labels and approval text or
confirmation text become user-visible run/history events. The persistence layer
checks again before committing those events. Rejected text is not a completed
result or a user message. Model responses and private checkpoints can retain
rejected content for correction and accounting; these are not presentation events.

The same run continues with bounded `output_validation_failed` feedback. The
model must rewrite with ordinary text/Unicode emoji in the requested language,
retaining meaning and verified facts. Already completed model steps keep their
original settlement and tools are not restarted. A new correction inference has
normal billing and admission. Rejections/corrections replay after worker restart.
Fresh and cached context paths both include the run's output instructions.
Existing step, context, time and budget limits apply. Exhaustion pauses with the
normal explicit reason and no rejected result. Unknown paid provider outcomes
retain the existing reconciliation protections.

## Approval presentation

A service description can contain unsupported markup even when model arguments
were valid. Loom keeps the prepared approval privately, including original
operation/backend IDs, terms, hash, call and absolute expiry. It does not publish
the rejected approval or call prepare again to correct its presentation.

Validation feedback supplies `approval_id` and the rejected description as
untrusted data. The policy-enabled `approval_present` model tool accepts
`{approval_id,text}`. It changes only display text. It does not change confirmation
text, parameters, binding, expiry, approval rules or grant consent. The model must
preserve the exact verified action/terms. Presentation resolution is recorded atomically with the public approval event,
so a later approval cannot resurrect an earlier rejected presentation. An invalid
exact confirmation text pauses with `approval_confirmation_unsupported`; Loom
cannot rewrite consent-bound text or renew the operation. The module must provide
a compatible confirmation contract before resumption.

Another business action or final result
cannot bypass the pending presentation. Expired presentation pauses with
`approval_presentation_expired`; no replacement operation is created.

A corrected presentation still requires normal fresh consent and dispatch-time
authorization. This contract does not establish Telegram delivery or actual-model
quality; consumer acceptance must exercise its rendering/delivery boundary.
