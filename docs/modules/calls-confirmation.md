<a id="calls-confirmation"></a>

# Calls and confirmation

[Guide index](../modules.md)

## RPC

Module endpoints use POST and an absolute path on the registered service origin. The broker supplies `Authorization: Bearer <delegated token>`; a subject in the request body cannot replace credentials. Read and prepare requests contain:

```json
{
  "protocol_version": 1,
  "operation_id": "opaque-id",
  "run_id": "opaque-id",
  "arguments": {}
}
```

A read gets a new operation ID per call. A durable write keeps its operation ID across preparation, execution and recovery. Execute adds `idempotency_key` equal to `operation_id` and a `confirmation` object with `mode` and, when applicable, `confirmation_id` or `preconditions`.

Return HTTP 200 with JSON containing the matching `operation_id`. A successful read or write uses `status: "succeeded"`, a `result` that matches `output_schema`, and optional `artifacts`. For writes, `accepted` or `pending` means the service owns the operation and the harness must check status. An execute response with `rejected` is terminal. Transport errors, non-200 responses, malformed results and unsupported statuses leave the write outcome uncertain. The runtime does not interpret a structured `error` or `not_started` field as proof that a write is safe to repeat.

The status endpoint receives `{"protocol_version":1,"operation_id":"opaque-id"}`. It can return `succeeded` with the committed result, `pending`/`accepted`, or terminal `failed`/`cancelled`. The runtime treats `not_found` as unknown: an absent record cannot prove that a delayed execute request will never commit. Recovery performs status lookup, not blind execution replay.

Every write/delete method must declare `idempotency: true`, `status_path` and `deduplication_window_seconds` of at least 86400 (24 hours). The service must retain deduplication and status records for its declared window. This is a contract requirement, not a promise of automatic replay for 24 hours. Keep uncertain operations available for reconciliation. `cancel_path` is accepted by the schema but the current write broker does not call it. Cancelling a run does not roll back a remote effect.

<a id="подтверждения"></a>

## Confirmation

The manifest supports three `confirmation.mode` values:

- `none`: the coordinator does not call a service prepare endpoint or request separate approval. Current user authority and policy still apply.
- `service_id`: prepare returns a service-generated `confirmation_id`. The service must bind it to the authenticated subject, method, operation, run and normalized arguments, then check that binding at execute.
- `exact_parameters`: prepare returns normalized `arguments` and opaque `preconditions`. The harness records a hash of the approved arguments and binding. Execute sends those arguments and preconditions; the service must check the preconditions atomically with the effect.

For either confirmed mode, prepare returns `status: "prepared"`, the matching `operation_id`, a future `expires_at` and a concise `description`. For `service_id`, `arguments` can be omitted to keep the original arguments; `confirmation_id` is required. For `exact_parameters`, `arguments` and non-null `preconditions` are required. Descriptions are limited to 4000 bytes. Preparation must not apply the business operation, although it can persist preparation records. Never put credentials in its description.

The coordinator stores approval against the prepared operation. Model text alone does not approve execution. A material change to price, time, recipient or other arguments requires a new preparation and approval. JSON number representations are preserved; RFC 8785/JCS compatibility is not claimed.

<a id="способ-ответа-пользователя"></a>

### User reply mode

`confirmation.mode` binds preparation to execution; it does not select buttons or messages. A confirmed method normally inherits the run's reply mode. Set `confirmation.reply_mode: "protocol"` to require a protocol decision: a native approval callback when `interaction.approvals` is true, otherwise the exact text fallback. A manifest cannot set `agent` or `exact_text`; the former is a run setting and the latter is selected by the runtime. A read method or a method with mode `none` cannot set this override.

```json
{
  "confirmation": {
    "mode": "service_id",
    "prepare_path": "/internal/agent/prepare",
    "reply_mode": "protocol"
  }
}
```

This is a method fragment, not a complete manifest. Reply mode is part of the contract hash. Permissions, expiry and preconditions are checked independently of the reply mechanism. See [Conversational input](../conversational-input.md) for the client contract and selection rules.

The model calls `module_call({module,method,arguments,confirmation_text})` for an external action. `confirmation_text` is a positive phrase in the user's language, separate from business arguments, limited to 2000 UTF-8 bytes. A method with the `protocol` override requires it with or without native callbacks. Other methods allow an empty string. In the exact text fallback, Loom adds no code and does not normalize the phrase; any different nonempty message rejects the pending action. An ordinary inherited protocol action without callbacks stays blocked. One unresolved action is allowed per chat.

Method help must explain the effect, material arguments, cost, limits, preconditions and confirmation requirement. The agent needs enough information to explain exactly what the user will authorize.
