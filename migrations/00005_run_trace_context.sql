-- +goose Up
ALTER TABLE runs
 ADD COLUMN trace_origin jsonb NOT NULL DEFAULT '{}' CHECK (octet_length(trace_origin::text) <= 4096),
 ADD COLUMN trace_attempt jsonb NOT NULL DEFAULT '{}' CHECK (octet_length(trace_attempt::text) <= 4096),
 ADD COLUMN trace_applied jsonb NOT NULL DEFAULT '{}' CHECK (octet_length(trace_applied::text) <= 8192),
 ADD COLUMN trace_step jsonb NOT NULL DEFAULT '{}' CHECK (octet_length(trace_step::text) <= 4096);
ALTER TABLE run_inputs
 ADD COLUMN trace_context jsonb NOT NULL DEFAULT '{}' CHECK (octet_length(trace_context::text) <= 4096),
 ADD COLUMN trace_sequence bigint GENERATED ALWAYS AS IDENTITY;

-- +goose Down
ALTER TABLE run_inputs DROP COLUMN trace_context, DROP COLUMN trace_sequence;
ALTER TABLE runs DROP COLUMN trace_origin, DROP COLUMN trace_attempt, DROP COLUMN trace_applied, DROP COLUMN trace_step;
