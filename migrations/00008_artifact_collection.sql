-- +goose Up
ALTER TABLE artifacts ADD COLUMN collected_at timestamptz;
CREATE INDEX artifacts_pending_collection ON artifacts(deleted_at,id) WHERE deleted_at IS NOT NULL AND collected_at IS NULL;
-- +goose Down
DROP INDEX artifacts_pending_collection;
ALTER TABLE artifacts DROP COLUMN collected_at;