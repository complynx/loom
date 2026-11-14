-- +goose Up
-- One row per user and organization. An absent row denies external agent access.
CREATE TABLE external_agent_profiles (
    owner_id uuid NOT NULL REFERENCES users(id),
    org_id text NOT NULL CHECK (org_id <> ''),
    enabled boolean NOT NULL,
    history boolean NOT NULL,
    memory boolean NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    confirmed_by text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (owner_id, org_id)
);

-- +goose Down
DROP TABLE external_agent_profiles;
