-- +goose Up
CREATE TABLE context_summaries(chat_id uuid PRIMARY KEY REFERENCES chats(id),through_seq bigint NOT NULL CHECK(through_seq>=0),text text NOT NULL,source_hash text NOT NULL,model_id text NOT NULL,created_at timestamptz NOT NULL DEFAULT now());
-- +goose Down
DROP TABLE context_summaries;