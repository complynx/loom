# Runs and input

[API index](../api.md)

## Create a run

Send `POST /v1/runs` with `runs.create`, `chat.read`, JSON and a persisted
`Idempotency-Key`. This example creates a conversational run in the default chat:

```json
{
  "input": {"text": "Help me book a massage", "artifact_ids": []},
  "interaction": {"reply_mode": "agent", "questions": "choices_and_text", "approvals": true},
  "disconnect_policy": "continue",
  "delivery": {"mode": "async", "wait_ms": 0},
  "budget": {"max_microcredits": 1000000}
}
```

`chat_id` is optional; omission uses the default chat. Input needs nonblank text
or at least one valid owned artifact; at most 16 artifact IDs are accepted.
The server saves an authorization session if none is selected. Service callers
must use the [delegation headers](authentication-routes.md#authentication).
`auth_session_id` selects an existing session. Billing currently supports only the
user's own account: omit `billing_account_id` or supply that user's internal ID;
another payer returns 403.

Clients can opt into [output validation](output-validation.md) with
`output_policy:{"disallow_custom_emoji":true}`. The policy is saved with the run
and is part of the idempotency request. Omission preserves existing output behavior.

Model and effort come from user preferences; `model` is not an accepted request
field. A positive `budget.max_microcredits` can only lower the allowed run limit;
zero or omission uses that limit. `disconnect_policy` defaults to `continue`;
`pause` additionally requires `runs.control`.

The normal response is `202` with the full run object and
`Location: /v1/runs/{run_id}`. The object includes `run_id`, `chat_id`, `status`,
`version`, `interaction` and budget/usage fields. Run and initial message commit
before success. Read `GET /v1/runs/{id}` for state and result; subscribe at
`GET /v1/runs/{id}/events`.

A retry with the same key and semantically identical creation body returns the
same run; a different body returns `409 idempotency_conflict`. Existing retries
are checked before `chat_busy`. One nonterminal run owns a chat: submit subsequent
messages to that run.

`delivery.mode=wait` waits up to `wait_ms` (maximum 30000 ms, possibly lower by
configuration). It returns `200` on a terminal state, including failure/cancellation,
or `202` at timeout. This changes delivery only. A lost HTTP response does not cancel
work; retry with the original key.

Optional `previous_run_id` identifies a terminal run of the same chat, owner and
organization; it does not resume that run. An active previous run returns
`409 previous_run_active`. A compatible completed run may reuse a saved history
prefix for up to 15 minutes. Attached files on either run disable this prefix reuse.
If reuse is unavailable or incompatible, normal context is built. Reuse does not
copy tool effects or guarantee a provider cache discount.

## Initial discovery expansion

For runs that must use only native files and must not consult business modules,
use the [native file execution profile](execution-profiles.md). Discovery expansion
below changes presentation only.

Run creation optionally accepts `discovery.expand_services`, for example:

```json
{"input":{"text":"Help me book an appointment"},"discovery":{"expand_services":["booking-service"]}}
```

Use registered `service_id` values, not module IDs or display names. At most 10 unique IDs are accepted; `$loom` selects builtins. IDs are exact, without wildcards. Invalid values return `422 invalid_discovery`. Omission keeps the compact service/category overview. A requested service adds a bounded first page of its currently authorized method-purpose hints. It never adds full contracts or grants permission. Unknown and inaccessible services both return empty expansions.

The run saves this presentation preference. Restart and subsequent inputs use the same preference; changing it on a retry with the same idempotency key returns `409 idempotency_conflict`. Submit it when creating each new run; it does not set a user-wide default. See [help discovery](../modules/authoring-help.md#reading-help) for page bounds and continuation.

Upgrade Loom before clients send this field. Older binaries reject it as an unknown request field. Before rolling back, stop sending it and remove the new manifest summary fields described in the module authoring guide.

## Run input

Send `POST /v1/runs/{id}/input` with `runs.control` and `Idempotency-Key`:

```json
{"kind":"steering","text":"Only consider evening appointments"}
```

With `interaction.reply_mode=agent`, use steering for ordinary messages, question
answers and suggestion captions. Text wakes `waiting_input` or `waiting_approval`
work; running work applies it between steps. Paused, reconciling or `waiting_auth`
work still needs its lifecycle blocker resolved. Closing a form does not cancel
an ordinary action. See [Conversational input](../conversational-input.md) for
consent, presentation references and critical confirmation.

With omitted reply mode or `protocol`, question replies use stable IDs:

```json
{"kind":"answer","question_id":"QUESTION_ID","choice_ids":["CHOICE_ID"]}
```

Use `text` when the question allows it. The question's schema, expiry and ownership
are checked. `answer` in agent mode returns `409 use_steering`. A button label is
not a choice ID. `interaction.questions` accepts
`none|text|choices|choices_and_text`; omission means `none`. In protocol mode,
unsupported questions stay in `waiting_input / client_interaction_disabled`.
In agent mode the capability controls presentation, not acceptance of chat text.

Binary callbacks require `interaction.approvals=true` and an approval whose own
`reply_mode` is `protocol`, including critical approvals inside agent-mode runs:

```json
{"kind":"approval","approval_id":"APPROVAL_ID","decision":"approve"}
```

`reject` refuses the action. Ordinary agent approvals require steering. Without
callbacks, ordinary protocol approvals remain blocked with
`client_interaction_disabled`; methods explicitly requiring protocol confirmation
use an `exact_text` fallback. Any other nonempty message rejects that critical
action. Unsupported interaction never implies consent.

Optional steering `presentation` may contain `reply_to`, `question_id` and
`approval_id` actually observed by the client. They carry context, not permission
or proof of delivery. Do not invent them from current server state.

The input response contains `run_id`, `status`, optional `reason` and `version`.
Callers with `chat.read` also receive `input_id`; conversational steering includes
a server-stamped `context`:

```json
{
  "run_id": "RUN_ID",
  "status": "queued",
  "version": 12,
  "input_id": "INPUT_ID",
  "context": {"revision": 3, "source_actor": "ACTOR", "approval_id": "APPROVAL_ID"}
}
```

A retry returns the original input ID even after later messages. Legacy receipts
may lack it. `input.applied` links durable processing to `input_id`.
`interaction.resolved` reports the agent resolver's or exact-text fallback's saved
decision; native callbacks do not universally emit it. Neither proves a business
effect. Refresh the run for its result, `question` and `approval`; the latter two
can coexist during clarification.

`POST /v1/runs/{id}/pause`, `/resume` and `/cancel` are direct controls; each requires
`runs.control` and `Idempotency-Key`. Send no body or `{}`. The receipt contains only
`run_id`, `status`, optional `reason` and `version`. Steering does not roll back
completed effects; cancellation cannot guarantee rollback.

## Passive events

`POST /v1/chats/{id}/events` requires `chat.events.append`. The `default` alias is
also supported here:

```json
{
  "source_event_id":"ui-event-123",
  "type":"user.action",
  "action":"massage.specialist.selected",
  "text":"Selected specialist Alex",
  "data":{"specialist_id":"alex-id","flow_id":"booking-123","step":"specialist_selected"},
  "occurred_at":"2026-10-10T18:00:00Z"
}
```

Authorization supplies actor and subject. The pair `(actor_id, source_event_id)`
deduplicates submissions; changed content under the same pair conflicts. `201`
confirms durable storage and does not start a run. `occurred_at` is reported client
time; Loom sets `received_at` and sequence number. Late events do not move backward
into a range already read.
