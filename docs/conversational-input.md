# Conversational input and action confirmation

Loom accepts one stream of user messages. Clients do not classify text as answers,
approvals, corrections, cancellations, or changes of topic. Clicking a suggested
caption and typing it have the same meaning. Closing a form does not discard a
pending ordinary action.

## Run negotiation

```json
{
  "input": {"text": "Help me book a massage"},
  "interaction": {
    "reply_mode": "agent",
    "questions": "choices_and_text",
    "approvals": true
  }
}
```

`reply_mode=agent` enables conversational input. Unknown modes return
`422 unsupported_reply_mode`. The accepted run echoes its settings; the field is
also in `/openapi.json`. Omitted mode or `protocol` retains ID-bound `answer` and
`approval` inputs. In agent mode, all ordinary text is steering, including replies
to choice-only questions. Question capabilities describe presentation, not which
chat text can reach the agent. `answer` returns `409 use_steering` in this mode.

`approvals=true` additionally advertises dedicated binary callbacks. A client can
support both chat and native confirmation buttons, or chat only with `false`.

### Reply policy selection

`confirmation.mode` binds prepare/execute to service state; it is independent of
reply presentation. `none` does not create an approval. For confirmed methods:

| Method override | Run reply mode | Native callbacks | Emitted `approval.reply_mode` |
|---|---|---|---|
| Omitted | `agent` | Either | `agent` |
| Omitted | `protocol` or omitted | Yes | `protocol` |
| Omitted | `protocol` or omitted | No | `protocol`, blocked with `client_interaction_disabled` |
| `protocol` | Either | Yes | `protocol` |
| `protocol` | Either | No | `exact_text` |

Only `agent` and `protocol` are valid run modes. A method override is `protocol`
or omitted. `exact_text` is an output policy selected by Loom, not a run option.
An ordinary action in an agent-mode run still uses steering when native callbacks
are supported; callbacks are reserved for approvals whose own reply mode is protocol.

## One input stream

Send `POST /v1/runs/{id}/input` with an `Idempotency-Key`:

```json
{"kind":"steering","text":"Yes, but only for one person"}
```

The URL still addresses the existing `run_id`. Do not create another run for each
message: a pending run owns its chat and a new run would receive `409 chat_busy`.
No action ID or hidden tag is required in text. An optional `presentation` object
preserves client observations: `reply_to` (its message reference), `question_id`,
and `approval_id`. These are context, not consent. Do not invent references for
ordinary text. `reply_to` is opaque client context; Loom does not fetch the external
message or verify delivery. There is no separate delivery-ack endpoint. The
server-current action does not prove which UI the user saw.

Acceptance saves exact text, authenticated source actor, input ID, monotonic
run-local revision and the current question/action. Queue insertion and acceptance
share a transaction. Retry a lost receipt with the same key/body; a different body
conflicts. Callers with `chat.read` receive the original `input_id`; conversational
steering also has a stamped `context`. Older idempotency records may have no input
receipt. `conversation_revision` is not a history sequence or SSE cursor.
Acceptance is never approval; read `GET /v1/runs/{id}` for current state. Its
`question` and `approval` may coexist: a follow-up can put the run in `waiting_input`
while the same action stays blocked. Read `approval.reply_mode`, not just run status,
to choose its UI.

Text wakes the same run from `waiting_input` or `waiting_approval`. During
inference it is processed between steps. Wait publication checks for already
accepted messages so a concurrent question does not strand them. Messages in
paused, uncertain or `waiting_auth` runs do not bypass resume, reconciliation or
reauthorization.

| Accepted input | Current wait | Continuation |
|---|---|---|
| Steering in an agent-mode run | Question or ordinary action | Same run queues; the model interprets the message; the action stays blocked until resolution |
| Steering with a critical native form | Question or critical action in an agent-mode run | Dialogue queues; the action still needs its binary callback |
| Native `approval` callback | Its protocol approval; possibly a follow-up question in agent mode | Decision is saved; the effect is queued without language interpretation |
| Steering for `exact_text` | Pending critical action | Runtime accepts the valid exact phrase or rejects the action; then queues continuation |
| Steering while paused, reconciling or waiting for auth | Lifecycle blocker | Input is saved; it does not itself clear that blocker |

## Ordinary actions

The prepared effect stays blocked while the agent interprets messages. Exact
parameters, binding hash, operation ID, expiry, latest question and accepted message
are supplied outside compacted history. The original model call gets a blocked
result so the agent can continue talking without dispatching it.

The agent can ask a follow-up, discuss cost, refuse, or call
`interaction_resolve({input_id,approval_id,argument_hash,decision})`, with
`decision=approve|reject`. This is a model function, not a client endpoint or JS
module. Its help is available through `modules.help.read`.

The runtime checks the running lease, exact proposal, expiry and accepted input
revision. Messages from before that proposal or superseded by another accepted
message cannot resolve it. Conflicting presentation references also block it.
All accepted text remains context, but only a current input can grant consent.
For example, a yes followed by a correction cannot be reused as permission to
ignore that correction. Resolution, input link and operation decision commit together. Replaying the same
resolution call is idempotent.

For qualified consent, reject the old action, prepare corrected parameters and ask
consent for the new proposal. This implementation requires a fresh message for the
amended proposal; it does not transfer consent between preparations. Ambiguous yes
or cancellation requires clarification. Several unrelated messages can pass while
the same ordinary action stays pending.

Only one unresolved action is allowed per run, and only one run per chat. Resolve
it before preparing a replacement. Separate chats have separate input streams.

## Critical actions

A service can require deterministic confirmation for a method:

```json
{
  "confirmation": {
    "mode": "service_id",
    "prepare_path": "/prepare",
    "reply_mode": "protocol"
  }
}
```

This is a method fragment, not a complete manifest.

This requires a confirmed write/delete; it cannot accompany `mode=none` or a read.
The requirement is part of the contract hash and cannot be downgraded by the model.

The agent supplies `confirmation_text` in `module_call`, in the user's language.
It is required for critical methods with and without callbacks, with a maximum
of 2000 UTF-8 bytes; it may be empty for other methods.
Name the concrete action; avoid generic reusable yes. Business `arguments` stay
separate. Runtime preserves this text without translating it or adding codes.

With native callbacks, the approval has `reply_mode=protocol`. Render dedicated
action buttons. Send their callbacks directly, without model interpretation:

```json
{"kind":"approval","approval_id":"APPROVAL_ID","decision":"approve"}
```

`reject` refuses it. Chat can clarify while the native action stays blocked. The
callback in an agent-mode run also works while a follow-up question is displayed. Do not turn these
dedicated callbacks into ordinary captions.

Without native callbacks, the approval has `reply_mode=exact_text`. Show its
`confirmation_text` and tell the user: **any other message cancels the action**.
Submit the reply through the same steering stream. Runtime checks exact decoded
text, without trimming, case folding, normalization, or language classification.
Equality approves only while the preparation is valid and any supplied presentation
references match. Every other nonempty message rejects, including a question, a
qualified yes, an expired phrase, or an explicitly stale form reference. Empty
steering is invalid input and does not resolve an action. That decision commits
before the agent continues. No codes are added.

Only one action may wait at a time. Retire old forms/suggestions when resolved.
Plain text without a reply reference or one-use code cannot distinguish an identical
delayed phrase from a fresh one. Ordered delivery and serial presentation are the
client contract; use native callbacks if that ambiguity is unacceptable.

## Dispatch and recovery

Consent does not replace authorization. Dispatch still checks current user, actor,
org, service contract, parameters, preconditions, expiry and fresh rights. Under
the run lock it also refuses unapplied steering and consent from an older revision.
A correction accepted before dispatch intent cannot be overtaken by old consent.
After that boundary, cancellation cannot promise rollback; return/reconcile the
actual outcome.

A direct callback or exact-text reply records a decision and queues execution;
its HTTP receipt is not synchronous proof of the effect. Fresh authority or a
known outcome may still be needed. The agent can report the actual result after
execution; model-free confirmation means no model classification is required to
accept that decision.

Existing operation idempotency and status recovery remain authoritative. Unknown
effects must reconcile before replacement. Pause/cancel endpoints remain direct
operational controls; conversational words are interpreted by the agent except in
the critical exact-text fallback.

## Events and failures

`input.applied` identifies a processed steering input. `interaction.resolved` is
emitted by the agent resolver and exact-text fallback; it links `input_id`,
`approval_id` and `decision`, and the agent resolver also includes `argument_hash`.
It is not a universal acknowledgement for native callbacks or a business success
event. Native approval input is retained in chat history; refresh the run after its
receipt and lifecycle updates. Use durable SSE cursors for replay, not input revision.

Client HTTP errors include `422 unsupported_reply_mode`, `409 use_steering`,
`409 input_conflict`, `409 approval_expired`, and `409 idempotency_conflict` as
applicable to the input kind. The internal `interaction_resolve` tool returns errors
as tool data, for example `interaction_conflict`, `input_conflict`,
`consent_superseded`, `presentation_conflict`, `interaction_resolved`, or
`approval_expired`. These do not approve or dispatch a replacement operation.

See [API](api.md), [trust boundaries](security.md), [storage](data.md) and
[failure behavior](failures.md#сообщения-и-подтверждения-при-сбоях).

## Verification boundary

Deterministic HTTP/PostgreSQL tests cover transport, resolution and effect gates.
Scripted providers do not prove language interpretation. Real-model Russian/English
QA and Zinusya delivery/keyboard behavior remain separate acceptance. Deterministic
tests make no paid provider calls. [Local evidence and remaining gates](validation/2026-10-16-conversational-input/README.md).
