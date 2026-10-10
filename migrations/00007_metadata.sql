-- +goose Up
ALTER TABLE artifacts ADD COLUMN external_disclosure_allowed boolean NOT NULL DEFAULT false;
ALTER TABLE artifacts ADD COLUMN source_service_id text NOT NULL DEFAULT '';
ALTER TABLE chat_events ADD COLUMN action text NOT NULL DEFAULT '';
-- +goose Down
ALTER TABLE chat_events DROP COLUMN action;
ALTER TABLE artifacts DROP COLUMN source_service_id;
ALTER TABLE artifacts DROP COLUMN external_disclosure_allowed;