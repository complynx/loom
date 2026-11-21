-- +goose Up
ALTER TABLE external_agent_profiles ADD COLUMN skills boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE external_agent_profiles DROP COLUMN skills;
