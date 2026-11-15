# Conversation recovery

[API index](../api.md) · [Runs and input](runs-input.md)

Use this contract to send user messages to a retained chat, also after a consumer
loses its run pointer or must replace unusable work. A chat is one long
conversation; a run is one small action inside it. Each user message is a new
interaction: it continues the open run, replaces a run that cannot continue, or
starts a new round of work after a terminal run. A cancellation by the user is a
stop of that run only; Loom does not use it to restart work.
`GET /v1/capabilities` advertises `conversation_recovery.version: 1`.
This version accepts text only and retains the existing chat. Create the first
run with `POST /v1/runs`; persist its chat ID. No cross-chat context import exists.
Posting a message to an owned chat with no runs returns `409 no_run` without
accepting the text. Use the locator's `idle / create_run` action and put that
first message in ordinary run creation instead.

## Locate retained work

`GET /v1/chats/{id}/active-run` requires `chat.read`. Supply the chat UUID; resolve
the default chat with `GET /v1/chats/default` first. The response contains
`run_id`, `status`, optional `reason`, and `next_action`. It returns active work
first; otherwise it returns the most recently admitted run with `status: terminal`
and `next_action: send_message`. Only a chat with no runs returns `idle / create_run`.
Run admission uses durable chat event order, not transaction timestamps.
The locator checks current owner,
organization, actor and chat visibility. It does not expose another actor's run.
The locator applies the same checks as message delivery. If a message sent now
would meet a blocker, the locator reports that blocker with the same `reason` and
`next_action`; see [Money and actionable blockers](#money-and-actionable-blockers).
For example, a terminal run of a chat with removed legacy history reports
`blocked / run_order_unavailable`, and an empty wallet reports
`blocked / credits_exhausted`.
A run paused at its step limit returns `accepted / step_limit` with
`next_action: send_message`, because the next message wakes it. A pause request,
a pending cancellation or an unknown outcome keeps that run `blocked`.
A run paused with `budget_exhausted` worked to its allowance without a result.
It returns `accepted / budget_exhausted` with `next_action: ask_user_to_continue`:
ask the user whether to continue or what to do next, and send the reply as a
message. A run paused with `credits_exhausted` returns `accepted` with
`next_action: send_message` when the wallet has funds again. In both cases the
next message continues the conversation in a new run.
Any other paused run that no message blocker applies to returns `blocked`, its
own pause reason and `next_action: resolve_blocker_then_resume`.

For a terminal run, `send_message` starts a new round: an ordinary new run in
the same chat with the current run cap. It is not a replacement and has no
replacement count. `POST /v1/runs` remains available for a new request that needs
its own creation parameters.

A generic proxy 404 does not prove that a run disappeared. Keep the run pointer
and original requests through transient routing errors. Apply bounded retries;
then use the locator. Never query the database from the consumer.

## Retain and deliver a message

Send `POST /v1/chats/{id}/messages` with JSON, `Idempotency-Key`, and all three
permissions: `chat.read`, `runs.control`, `runs.create`.

```json
{"text":"Please continue the appointment request"}
```

Text must contain a non-whitespace character and fit in 65536 UTF-8 bytes;
otherwise the endpoint returns `422 invalid_message`. A `replace_run_id` that is
not a UUID returns `422 invalid_run_id`. The
key is scoped to chat, owner, organization and actor. It is independent of the
keys of `POST /v1/runs` and `POST /v1/runs/{id}/input`; a key used there neither
blocks nor satisfies a message. Persist the exact body and
key before sending. Reuse both after an unknown response or process restart.
Changing the body under the same key returns `409 idempotency_conflict`.

The endpoint finds the latest run in the retained chat. It submits steering to
usable nonterminal work and renews its step allowance through the ordinary input
path. An eligible `step_limit` pause wakes automatically. Other pause reasons,
authorization waits and protocol question/approval requirements remain in force.
Input never grants consent merely because recovery accepted it.

After a terminal run (`completed`, `failed` or `cancelled`), the message starts a
new round. The new run inherits the interaction settings of the latest run, gets
the current configured run cap, and has no `replaces_run_id`. The wallet remains
the shared money limit of the owner.

A run allowance bounds one action; it does not stop the conversation. A run that
pauses with `budget_exhausted` worked to its allowance without a result. Ask the
user whether to continue or what to do next; do not send a message for the user.
The reply of the user is an ordinary message. If the open
run is paused because its own allowance (`budget_exhausted`) or the wallet
(`credits_exhausted`) could not pay the next step, the message replaces that run:
Loom supersedes it and admits a linked successor. The wallet check applies to the
successor, so an empty wallet still returns `blocked / credits_exhausted`.

To replace an open run that is unusable for another reason, supply its observed ID:

```json
{"text":"Continue the same task","replace_run_id":"PREDECESSOR_UUID"}
```

A replacement links the successor to its predecessor (`replaces_run_id`), so the
successor gets the operation receipts of the unfinished work. A `replace_run_id`
that names the terminal latest run also creates a linked successor.
An explicit ID must identify the
latest run or its direct predecessor after a racing replacement. An invalid or
stale target returns `409 replacement_target_changed` before first acceptance.
Refresh the locator and submit a corrected request. If a previously accepted
target becomes stale, its message remains retained and can be explicitly abandoned.

The HTTP response is `202`; this means the message is durable, not that inference
or a business operation succeeded:

```json
{
  "accepted": true,
  "delivered": true,
  "abandoned": false,
  "message_id": "MESSAGE_UUID",
  "sequence": 42,
  "run_id": "RUN_UUID",
  "input_id": "INPUT_UUID",
  "status": "queued",
  "next_action": "read_run"
}
```

`delivered` means the text reached a run's initial input or durable steering
queue. `input_id` exists only for steering. `status: accepted` means the run still
waits for interaction. `status: queued` includes queued and currently executing
work; it does not promise that the next paid reservation will fit. A retry of a
delivered message returns its original IDs and the run's current status, including
`terminal`; it never delivers the text again.

Admission, text, run/job insertion and receipt commit together. If a policy blocks
delivery, the inbox message still commits. Chat locking serializes acceptance and
delivery with other messages, ordinary creation and chat deletion. `sequence`
records server acceptance order. Consumers must send their own source messages
in order; this endpoint cannot infer order from reversed network arrivals.

An undelivered earlier message blocks later delivery with
`earlier_message_pending` and `blocking_message_id`. Retain a consumer outbox
mapping message IDs to original keys and bodies. Resolve/retry the earlier
message first, then retry later messages. There is no automatic drain or retry
loop. A later call cannot silently skip retained input.

The ordering barrier covers the chat, not one actor. A chat can serve more than
one actor of the same owner. If another actor's message is pending, the endpoint
returns `blocked / other_actor_message_pending` with
`next_action: contact_original_actor` and no `blocking_message_id`. Only the
original actor can deliver or abandon that message.

`POST /v1/runs/{id}/input` does not check this barrier. Do not mix it with this
endpoint while the chat has retained undelivered messages; an ordinary input
would then be applied before earlier retained text.

## Explicitly withdraw an undelivered message

Send `POST /v1/chats/{id}/messages/{message}/abandon` with `Idempotency-Key` and
`chat.read` plus `runs.control`. The body is empty or `{}`. Only the original actor
in the same owner/organization/chat can withdraw it. The operation returns `200`
with `accepted: true`, `abandoned: true`, `delivered: false`,
`status: abandoned`, `reason: explicit_withdrawal` and
`next_action: continue_or_create_new_request`: retry the other retained messages
in order, or create a new request. Its original text, key, sequence and an audit record remain
durable. Repeating abandonment is safe. Replaying the original message returns its
abandoned receipt and never delivers it. Delivered messages return
`409 message_already_delivered`; abandonment does not cancel a run.

Explicit abandonment releases the ordering barrier. Use it when the user withdraws
a request or a target is obsolete. Never abandon automatically just to bypass an
unknown effect.
Unknown steps, write receipts and reservations remain attached to their runs.

For `run_order_unavailable`: abandon the retained message, then create the
request with `POST /v1/runs`. Put the request text in that creation body. Do not
replay the abandoned text or send it again as steering. Retry any other retained
messages in acceptance order.
Ordinary `POST /v1/runs` returns `409 pending_conversation_messages` while the
chat has any undelivered, unabandoned inbox message, so a later request cannot
pass retained text. An idempotent retry of a previously admitted creation
still returns its original run.

## Replacement safety and context

For a running predecessor, replacement requests cancellation, including during an
in-flight paid step, and returns
`recovery_pending / cancellation_pending`. Retry the same message after bounded
backoff. The request and cancellation flag survive restart. The caller must
bound its retry count and deadline; after that, show pending work and provide a
manual continuation action. Loom never replaces a run without a message, and one
message creates at most one run. There is no limit on successive replacements:
the user can continue unfinished work again.

Before requesting cancellation, Loom checks the saved session
and current model entitlement. A revoked session or disallowed model does not
cancel running work through this endpoint. The predecessor's own reservation is
not treated as wallet exhaustion during that preflight. Funds and admission are
checked again after cancellation; later policy changes can still block a successor.

Loom creates a successor only after there are no pending/unknown steps,
dispatching/pending/unknown writes, or outstanding run reservations. Otherwise it
returns `blocked / inference_or_effect_uncertain` with
`next_action: reconcile_predecessor`. Use the existing receipt/reconciliation
procedure; cancellation is neither rollback nor proof of no effect. The old
executor loses its lease and fence before the transaction commits. A successor
cannot dispatch while that transaction is uncommitted.
A sponsored `platform_provider_attempts` record does not block a replacement or
a new round: the platform accepted that liability, and the record stays on its
original run. The sponsored retry limit counts attempts for each run.

The successor of a replaced run has `replaces_run_id`; chat history also
records `run.replaced` before the successor's request. The model receives that
event as data, not as an instruction. It uses the same chat, owner and actor. Existing history,
source roles, operation receipts and saved summaries stay in place; they are not
converted to a new user instruction or confirmation. Normal bounded context
selection and retention still apply. Expired/deleted history is not restored.
Existing artifact references remain subject to current ownership, expiry and
authorization checks. Recovery does not copy artifact bytes or renew access.
No paid handoff summary is required.

A new round after a terminal run is an ordinary run. It reads the chat history
and gets no receipt handoff.

Before inference, a replacement successor also loads deterministic predecessor operation receipts
of at most three linked predecessors; older ones stay in chat history only. It rechecks their module/method and identity bindings
against current authority. The model receives historical call/tool-result pairs
with original run/call/operation IDs and known states, arguments and results.
`requested_arguments` preserves the original request; `arguments` contains the
effective prepared/executed parameters, falling back to the request when no
normalized preparation exists. Historical calls describe those effective terms.
Historical tool-result pairs follow the retained earlier history, including the
predecessor's own request, and precede the successor's current request. Current
method-access lookup is shared for matching module/method within
one handoff; every receipt's identity binding is checked. Authority is not cached
across turns or worker claims.
Disclosure does not require the old write contract to remain unchanged. A changed
contract or confirmation mode is labeled `contract_changed: true`; it never
authorizes redispatch or reuse of old consent. Any new effect needs the current
contract, a new prepare and fresh consent as required by that contract.
Context call IDs are namespaced to avoid collisions with current calls. Prior
confirmation tokens and consent are not imported. Known successful effects must
not be repeated; prepared actions need a new prepare and fresh consent.
The handoff is limited to 64 operations and 65536 encoded bytes. It fails closed
with `paused / recovery_context_unavailable` on a binding, retention or size
denial. The same pause applies when the module or method of a receipt is no
longer visible to the current principal: it was removed, or the permission for
the module was revoked. Loom does not distinguish these two cases, and it does
not retry them. A denied method permission or a rejected session uses
`waiting_auth / recovery_authorization_required`.
Restore current authority/context access and resume or reauthorize explicitly;
do not drop receipts or blindly repeat an effect. A size denial
(`recovery_context_too_large`) has no consumer remedy: cancel that run. The next
message then starts a new round. Custom engine integrations
without a receipt-authorizing callback can continue only chains with no operations.
Receipts whose predecessor admission event has been removed by retention/deletion
are not copied back into model context; recovery fails closed in that case too.
Transient storage/discovery failures propagate to the bounded worker retry path.
The complete request, including receipt overhead, still passes the ordinary
size/token-window checks before inference; if it cannot fit, execution pauses
with `context_unavailable` rather than dispatching an oversized paid request.

The successor inherits interaction, skills, execution profile, discovery,
output/disconnect policies and authorization session. Admission selects a model
from current preferences and policy, checks current run capacity, and validates
the saved session. Execution continues to recheck current authority. A rejected
admission rolls back cancellation/creation while retaining the message.

## Money and actionable blockers

The wallet is the shared money limit of the owner. Every run of the owner uses
it, and every paid step checks it. The per-run allowance only bounds one run.

Every run that this endpoint admits, a new round or a replacement, gets the
current configured run cap, as `POST /v1/runs` without a `budget` does. The
allowance that an earlier run spent does not limit it. A narrower `budget` of
the first `POST /v1/runs` is not copied.

Spent
credits remain on the original runs/ledger. Unknown steps, writes and reservations
must be reconciled before a replacement or a new round; they are never erased or
silently freed.
Every later paid step still checks the wallet and current policy atomically.
The step allowance renewed by a message is separate from money.
Steering counts the target run's existing reservation as available for that run;
an in-flight step holding the full wallet does not produce a false no-funds reply.

Each blocker of a message has one `next_action`. The message endpoint and the
locator both use this table:

| Reason | `next_action` | Consumer action |
| --- | --- | --- |
| `credits_exhausted` | `fund_wallet_then_retry` | Fund the existing wallet, then retry the same message. No inference is required to report this condition. |
| `run_order_unavailable` | `create_run` | Retention removed legacy admission events, so the latest run of this chat is not proven. Do not guess a predecessor. Abandon the retained message and create the request with `POST /v1/runs`. |
| `authorization_required` | `reauthorize_session` | Reauthorize the retained session with the same owner and actor, then retry. |
| `model_not_allowed` | `select_allowed_model_or_restore_access` | Select an allowed model/effort or restore model entitlement, then retry; running work is not cancelled by this preflight denial. |
| `inference_or_effect_uncertain` | `reconcile_predecessor` | Reconcile the predecessor's unknown outcome; do not repeat its effect. |
| `replacement_target_changed` | `abandon_message_or_inspect_run` | A previously accepted target is stale. Refresh the locator; abandon the message if it is obsolete. |
| `earlier_message_pending` | `retry_earlier_message` | Retry the message in `blocking_message_id` first. |
| `other_actor_message_pending` | `contact_original_actor` | Another actor must deliver or abandon its message. |
| `active_run_limit`, `total_run_limit`, other codes | `resolve_blocker_then_retry` | Wait for capacity or change the existing authorized policy, then retry the same message. |

`recovery_pending / cancellation_pending` is not a blocker; its `next_action` is
`retry_same_message`. A paused run is a different condition from a blocked
message. When no blocker in the table applies, it reports its own pause reason
with `next_action: resolve_blocker_then_resume`: resolve the cause, then resume
that run. Money pauses are the exception: the next message replaces a run paused
with `budget_exhausted` or `credits_exhausted`, as described above.

`accepted: true, delivered: false` distinguishes retained text from an applied
input. `blocked` can also accompany `delivered: true` when the receiving run has
a lifecycle blocker. Read that run and resolve its existing control/session flow.
Do not resubmit delivered text with a new key. Authentication/permission failures
before acceptance return ordinary 401/403/404 and do not accept a message.

Do not create runs or cancel work in an automatic loop after a transport error.
Use the locator and retry the original message. No new monthly cap or funding
policy is introduced.

## Upgrade and rollback

Migration 00010 adds an inbox table and backfills admission/input sequence metadata
from retained chat events. It preserves existing run state, receipts and ledger
amounts. A legacy run whose admission event retention removed has no known
position. The order of the terminal runs in its chat is then not proven, so the
migration gives none of them a sequence; recovery from such a terminal run returns
`run_order_unavailable` instead of guessing. A nonterminal run is always the
latest run of its chat and keeps its sequence. Every run admitted after the
migration has a sequence, so one ordinary `POST /v1/runs` restores recovery for
that chat.
Apply migrations before API/workers as described in [operations](../operations.md).
Stop older API/worker binaries before accepting recovery traffic: they cannot
preserve the new replacement metadata when rewriting run documents. Enable
consumer calls only after the server advertises the capability. Rollback:
stop recovery calls and workers first; an older binary can read the existing
runs, but must not execute recovery-chain runs or advance undelivered inbox messages.
Preserve the table and its
receipts for a later upgrade. Do not run its destructive down migration on retained
customer messages. Deleting a chat also deletes its recovery inbox and denies
subsequent reads/retries.

Follow the [QA guide](../qa.md) for installation acceptance. Fixture coverage is
separate from live-provider, consumer and production rollout verification.
