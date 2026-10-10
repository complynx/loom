-- +goose Up
ALTER TABLE idempotency_records ADD COLUMN request_doc jsonb;
CREATE INDEX artifacts_owner_index ON artifacts(owner_id);
-- +goose Down
DROP INDEX artifacts_owner_index;
ALTER TABLE idempotency_records DROP COLUMN request_doc;