-- +goose Up
ALTER TABLE external_agent_profiles ADD COLUMN writes boolean NOT NULL DEFAULT false;
ALTER TABLE write_operations ALTER COLUMN run_id DROP NOT NULL;
ALTER TABLE write_operations ADD COLUMN external_session_id uuid;
ALTER TABLE write_operations ADD CONSTRAINT write_operation_owner CHECK (
    (run_id IS NOT NULL) <> (external_session_id IS NOT NULL)
);
CREATE UNIQUE INDEX external_operation_call ON write_operations(external_session_id,call_id);
CREATE INDEX external_operation_waiting ON write_operations(external_session_id)
    WHERE state IN ('preparing','prepared');

CREATE TABLE external_agent_approval_rules (
    session_id uuid NOT NULL REFERENCES external_agent_sessions(id) ON DELETE CASCADE,
    target text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (session_id,target)
);

-- +goose Down
-- Refuse downgrade while a session-owned receipt still exists.
ALTER TABLE write_operations ALTER COLUMN run_id SET NOT NULL;
DROP TABLE external_agent_approval_rules;
ALTER TABLE write_operations DROP CONSTRAINT write_operation_owner;
ALTER TABLE write_operations DROP COLUMN external_session_id;
ALTER TABLE external_agent_profiles DROP COLUMN writes;
