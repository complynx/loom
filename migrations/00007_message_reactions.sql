-- +goose Up
-- Current state is compact; immutable message.reaction events retain changes.
ALTER TABLE chat_events ADD COLUMN reactions jsonb NOT NULL DEFAULT '{}';
ALTER TABLE chat_events ADD COLUMN reactions_seq bigint NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE chat_events DROP COLUMN reactions;
ALTER TABLE chat_events DROP COLUMN reactions_seq;
