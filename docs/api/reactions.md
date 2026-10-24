# Message reactions

[API index](../api.md)

Reactions are optional. They do not start inference, wake a waiting run, count as
steering, or grant consent. User reactions and the assistant's reactions are
independent. A reaction targets a message by its chat history `seq`, never by a
run SSE sequence or an external platform message ID.

## Declare client support

Include `interaction.reactions` in `POST /v1/runs`:

```json
{
  "input": {"text": "Help me choose"},
  "interaction": {
    "reactions": {
      "emoji": ["👍", "❤️"],
      "custom": [{"id": "thanks", "description": "A small thank you"}]
    }
  }
}
```

Omit the field or send `null` or `{}` when the client cannot display bot
reactions. Loom then omits the `message_react` model function and rejects its
execution. `all_emoji:true` permits the full emoji repertoire; `emoji` permits
only the listed literal values. `custom` permits only the listed IDs. Combine
either emoji option with `custom`, or use custom reactions alone. The policy is
saved with the run and cannot change during that run.

The model function description includes the accepted policy, so the agent sees
support and allowed values on its first turn. Custom descriptions are untrusted
client data. They cannot change system instructions or grant authority.
The built-in `$tools.message_react` help card explains use and recovery.

Values use the literal emoji string, or `custom:<id>` for a custom reaction.
IDs use 1–64 ASCII letters, digits, underscores or hyphens; descriptions contain
1–256 UTF-8 bytes. A policy permits at most 256 entries. Values contain at most
128 UTF-8 bytes. Emoji identifiers must contain a Unicode symbol or an emoji
sequence marker, without whitespace, controls or ordinary letters. Loom does
not maintain a versioned Unicode emoji registry; clients own rendering and
platform-specific emoji compatibility. Variation selectors and skin tones are
significant: use the exact advertised value.

## Record a user reaction

Send `POST /v1/chats/{id}/reactions` with `chat.events.append`, `chat.read` and
access to the chat. The `default` chat alias is supported.

```json
{"source_event_id":"reaction-42","message_seq":12,"reaction":"👍","active":true}
```

Success returns `201` and the durable `message.reaction` chat event. Set
`active:false` to remove the same value. Use a new `source_event_id` for each
user action, including re-adding a removed reaction. Keep each ID and exact body
until its receipt is known. Repeating them returns the original event without
changing the current state; changing the body for the same ID returns
`409 event_conflict`. IDs contain 1–128 bytes and share the caller's passive-event
ID namespace. Client negotiation limits the bot, not imported user reactions.
The authenticated caller is recorded; a request cannot choose another actor or
overwrite the assistant's state.

Targets can be `message.user`, `message.assistant`, `input.steering`,
`input.answer`, `question.created`, or `approval.created`. Other targets return
`422 invalid_reaction_target`; a missing or inaccessible target returns `404`.
At most 32 values per actor and 16 KiB of total reaction state fit on one message;
exceeding either limit returns `409 reaction_limit`. Setting an already active
value or removing an absent value preserves the state and records the accepted
action under its new source ID.

## Bot reactions and client delivery

The agent calls this standalone model function, outside JavaScript:

```js
message_react({message_seq:12,reaction:"custom:thanks",active:true})
```

The runtime validates the run lease, target and saved policy. It commits state,
history, receipt and the run SSE `message.reaction` event together. A retry with
the same tool call ID returns the saved receipt and does not publish another SSE
event. Unsupported values return `reaction_not_supported` to the model.

The SSE data is the complete chat event shown below. A client maps
`data.message_seq` to its displayed message, applies `data.active` for
`data.actor`, and deduplicates by the enclosing run SSE sequence. Preserve the
mapping between Loom sequences and external message IDs in the client.
`message.completed` SSE data includes `text` and `message_seq` for the final
assistant message; history supplies target IDs for other messages. A saved
reaction confirms protocol acceptance, not rendering by an external platform.
User actions are available through chat history; they do not create run SSE work.

## History and model context

Each accepted action is a separate chat event with empty `text` and this `data`:

```json
{"message_seq":12,"reaction":"👍","active":true,"actor":"user:alice"}
```

`source_actor` retains the authenticated caller (`agent` for bot actions).
Reaction actors use `user:<authenticated actor>` or `assistant`. Each original
message also carries its current state, omitted when empty:

```json
{"seq":12,"type":"message.assistant","text":"Done","reactions_seq":15,"reactions":{"user:alice":["👍"],"assistant":["custom:thanks"]}}
```

Each actor's values are unique and sorted. Clients render custom IDs with their
declared catalog; descriptions do not repeat beside every attached value. A bot's
custom reaction change event also retains its short `description` from the saved
policy, so history keeps its meaning after that run ends.
The attached state is current at read time, even when the history page ends
before the corresponding change event. Each changed message includes
`reactions_seq`, the chat sequence of its last accepted reaction action. It
remains present after the last reaction is removed. Apply only change events
whose chat `seq` exceeds that watermark, then advance it. This prevents delayed
events from overwriting a newer snapshot. Run SSE `seq` is a different cursor; use the chat `seq` inside its data.

History pages, search and bounded history excerpts expose this state. Under a
small output budget an excerpt can omit it and marks the event as truncated;
text-only `event_seq` reads omit it. Read an ordinary history page for complete
metadata. Model history includes message sequence metadata, attached reactions
and separate change events as data. Existing compaction and history limits still
apply. Reaction changes invalidate saved history-prefix caches. Stored messages
and reaction events follow normal chat deletion and retention rules.

## Schema compatibility

Migration `00007_message_reactions.sql` adds a JSONB projection to chat events;
existing messages start with no reactions. No existing event data is rewritten.
Upgrade API and workers together before advertising support. Older workers do
not know the model function. Startup does not downgrade the database. For rollback,
stop new workers and stop advertising reactions; the added column can remain in
place for older binaries. Do not drop it if reaction state must be preserved.
