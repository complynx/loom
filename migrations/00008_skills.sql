-- +goose Up
-- Deleted rows keep their version, preventing stale edits after recreation.
CREATE TABLE skills (
    org_id text NOT NULL,
    name text NOT NULL,
    usecase text NOT NULL,
    body text NOT NULL,
    require jsonb NOT NULL DEFAULT '[]',
    version bigint NOT NULL CHECK (version > 0),
    deleted boolean NOT NULL DEFAULT false,
    PRIMARY KEY (org_id, name)
);

-- +goose Down
DROP TABLE skills;
