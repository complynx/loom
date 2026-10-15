-- +goose Up
CREATE TABLE identity_cache_state (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 epoch bigint NOT NULL DEFAULT 0 CHECK(epoch >= 0)
);
INSERT INTO identity_cache_state(singleton) VALUES(true);
-- +goose Down
DROP TABLE identity_cache_state;
