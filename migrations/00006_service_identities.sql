-- +goose Up
CREATE TABLE service_identities(issuer text NOT NULL,subject text NOT NULL,audience text NOT NULL,first_seen_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(issuer,subject,audience));
-- +goose Down
DROP TABLE service_identities;