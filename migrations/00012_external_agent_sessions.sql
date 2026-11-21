-- +goose Up
CREATE TABLE external_agent_sessions (
    id uuid PRIMARY KEY,
    transport_id uuid NOT NULL UNIQUE,
    owner_id uuid NOT NULL,
    org_id text NOT NULL,
    client_id text NOT NULL CHECK (client_id <> ''),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL DEFAULT clock_timestamp() + interval '24 hours',
    FOREIGN KEY (owner_id, org_id) REFERENCES external_agent_profiles(owner_id, org_id) ON DELETE CASCADE
);
CREATE INDEX external_agent_sessions_owner ON external_agent_sessions(owner_id, org_id);
CREATE INDEX external_agent_sessions_expiry ON external_agent_sessions(expires_at);

-- +goose Down
DROP TABLE external_agent_sessions;
