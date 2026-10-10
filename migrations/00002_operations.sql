-- +goose Up
CREATE TABLE write_operations (
    id uuid PRIMARY KEY,
    run_id uuid NOT NULL REFERENCES runs(id),
    call_id text NOT NULL,
    state text NOT NULL CHECK (state IN ('preparing','prepared','approved','rejected','dispatching','pending','unknown','succeeded','failed')),
    doc jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(run_id, call_id)
);
CREATE INDEX write_operations_reconcile ON write_operations(updated_at)
    WHERE state IN ('dispatching','pending','unknown');

-- +goose Down
DROP TABLE write_operations;
