-- +goose Up
CREATE TABLE external_agent_authorizations (
    id uuid PRIMARY KEY,
    owner_id uuid NOT NULL,
    org_id text NOT NULL,
    client_id text NOT NULL,
    client_name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    FOREIGN KEY (owner_id, org_id) REFERENCES memberships(owner_id, org_id) ON DELETE CASCADE
);
CREATE INDEX external_agent_authorizations_owner ON external_agent_authorizations(owner_id, org_id);
CREATE INDEX external_agent_authorizations_expiry ON external_agent_authorizations(expires_at);
ALTER TABLE external_agent_sessions ADD COLUMN authorization_id uuid
    REFERENCES external_agent_authorizations(id) ON DELETE CASCADE;
CREATE INDEX external_agent_sessions_authorization ON external_agent_sessions(authorization_id);

-- +goose Down
ALTER TABLE external_agent_sessions DROP COLUMN authorization_id;
DROP TABLE external_agent_authorizations;
