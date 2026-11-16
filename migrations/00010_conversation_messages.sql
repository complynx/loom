-- +goose Up
CREATE TABLE conversation_messages (
    id uuid PRIMARY KEY,
    seq bigserial UNIQUE NOT NULL,
    chat_id uuid NOT NULL REFERENCES chats(id),
    owner_id uuid NOT NULL REFERENCES users(id),
    org_id text NOT NULL,
    actor_id text NOT NULL,
    key text NOT NULL,
    request jsonb NOT NULL,
    run_id uuid REFERENCES runs(id),
    input_id uuid,
    delivered boolean NOT NULL DEFAULT false,
    abandoned boolean NOT NULL DEFAULT false,
    CHECK (NOT (delivered AND abandoned)),
    UNIQUE(chat_id, owner_id, org_id, actor_id, key)
);
CREATE INDEX conversation_messages_pending ON conversation_messages(chat_id, seq) WHERE NOT delivered AND NOT abandoned;

-- Use durable chat order, not transaction-start timestamps. Retention may have
-- removed the admission event of a legacy run. Such a run has no known position,
-- so the order of the terminal runs in its chat is not proven. Leave those runs
-- without a sequence; recovery then reports run_order_unavailable. A nonterminal
-- run keeps its sequence only where its source event survives. Use the first
-- matching event when legacy history contains duplicate source events.
WITH unordered AS (
    SELECT DISTINCT o.chat_id FROM runs o WHERE NOT EXISTS(
        SELECT 1 FROM chat_events oe
        WHERE oe.chat_id=o.chat_id AND oe.run_id=o.id AND oe.kind='message.user')
), admissions AS (
    SELECT chat_id,run_id,min(seq) AS seq FROM chat_events
    WHERE kind='message.user' GROUP BY chat_id,run_id
)
UPDATE runs r SET doc=jsonb_set(r.doc,'{admission_sequence}',to_jsonb(ce.seq))
FROM admissions ce
WHERE ce.chat_id=r.chat_id AND ce.run_id=r.id
AND (r.state NOT IN('completed','cancelled','failed')
    OR NOT EXISTS(SELECT 1 FROM unordered u WHERE u.chat_id=r.chat_id));
UPDATE run_inputs ri SET doc=jsonb_set(ri.doc,'{history_sequence}',to_jsonb(ce.seq))
FROM (
    SELECT e.run_id,e.doc->>'id' AS input_id,min(e.seq) AS seq
    FROM chat_events e JOIN runs r ON r.id=e.run_id AND r.chat_id=e.chat_id
    WHERE e.kind IN('input.steering','input.answer','input.approval')
    GROUP BY e.run_id,e.doc->>'id'
) ce WHERE ce.run_id=ri.run_id AND ce.input_id=ri.id::text;

CREATE INDEX runs_chat_recovery_order ON runs (
    chat_id,
    (state NOT IN('completed','cancelled','failed')) DESC,
    (COALESCE((doc->>'admission_sequence')::bigint,0)) DESC,
    id DESC
);

-- +goose Down
DROP INDEX runs_chat_recovery_order;
DROP TABLE conversation_messages;
