-- +goose Up
ALTER TABLE artifacts ADD COLUMN source_module text NOT NULL DEFAULT '';
ALTER TABLE artifacts ADD COLUMN source_method text NOT NULL DEFAULT '';
ALTER TABLE artifacts ADD COLUMN source_fetch_method text NOT NULL DEFAULT '';
ALTER TABLE artifacts ADD COLUMN source_reference_id uuid REFERENCES artifacts(id);
CREATE TABLE artifact_file_refs (
 id uuid PRIMARY KEY REFERENCES artifacts(id),
 current_artifact_id uuid NOT NULL REFERENCES artifacts(id),
 lease_id uuid,
 lease_until timestamptz
);
-- +goose Down
DROP TABLE artifact_file_refs;
ALTER TABLE artifacts DROP COLUMN source_reference_id;
ALTER TABLE artifacts DROP COLUMN source_fetch_method;
ALTER TABLE artifacts DROP COLUMN source_method;
ALTER TABLE artifacts DROP COLUMN source_module;
