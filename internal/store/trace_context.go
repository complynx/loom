package store

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/telemetry"
)

type appliedTrace struct {
	Revision int64                  `json:"revision"`
	Sequence int64                  `json:"sequence"`
	Parent   telemetry.TraceContext `json:"parent"`
	Inputs   []string               `json:"inputs"`
}

type StepTrace struct {
	Origin   telemetry.TraceContext
	Parent   telemetry.TraceContext
	Previous telemetry.TraceContext
	Inputs   []telemetry.TraceContext
	Revision int64
}

func decodeAppliedTrace(raw []byte) appliedTrace {
	var value appliedTrace
	if len(raw) > 8192 || json.Unmarshal(raw, &value) != nil {
		return appliedTrace{}
	}
	if len(value.Inputs) > telemetry.MaxTraceInputs {
		value.Inputs = nil
	}
	for _, id := range value.Inputs {
		if uuid.Validate(id) != nil {
			value.Inputs = nil
			break
		}
	}
	return value
}

// RunTrace ignores accepted but unconsumed input. Diagnostic values grant no authority.
func (s *Store) RunTrace(
	ctx context.Context,
	runID string,
) (telemetry.TraceContext, telemetry.TraceContext, telemetry.TraceContext, error) {
	var origin, previous, raw []byte
	err := s.pool.QueryRow(ctx, "SELECT trace_origin,trace_attempt,trace_applied FROM runs WHERE id=$1", runID).
		Scan(&origin, &previous, &raw)
	applied := decodeAppliedTrace(raw)
	return telemetry.DecodeTrace(origin), telemetry.DecodeTrace(previous), applied.Parent, err
}

// ExecutionTrace restores the exact applied input IDs, not the latest received request.
func (s *Store) ExecutionTrace(ctx context.Context, runID string) (StepTrace, error) {
	var result StepTrace
	var origin, previous, raw []byte
	if err := s.pool.QueryRow(ctx, "SELECT trace_origin,trace_step,trace_applied FROM runs WHERE id=$1", runID).
		Scan(&origin, &previous, &raw); err != nil {
		return result, err
	}
	applied := decodeAppliedTrace(raw)
	result.Origin = telemetry.DecodeTrace(origin)
	result.Previous = telemetry.DecodeTrace(previous)
	result.Parent = applied.Parent
	result.Revision = applied.Revision
	if len(applied.Inputs) == 0 || len(applied.Inputs) > telemetry.MaxTraceInputs {
		return result, nil
	}
	rows, err := s.pool.Query(
		ctx,
		"SELECT trace_context FROM run_inputs WHERE run_id=$1 AND id=ANY($2::uuid[]) AND applied ORDER BY trace_sequence",
		runID,
		applied.Inputs,
	)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var carrier []byte
		if err = rows.Scan(&carrier); err != nil {
			return result, err
		}
		result.Inputs = append(result.Inputs, telemetry.DecodeTrace(carrier))
	}
	return result, rows.Err()
}

// RecordTraceStep commits diagnostic ancestry under the existing execution fence.
func (s *Store) RecordTraceStep(
	ctx context.Context,
	runID, owner string,
	fence int64,
	carrier telemetry.TraceContext,
) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := leaseRun(ctx, tx, runID, owner, fence); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "UPDATE runs SET trace_step=$2 WHERE id=$1", runID, document(carrier))
		return err
	})
}

func applyInputTrace(ctx context.Context, tx pgx.Tx, runID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if len(ids) > telemetry.MaxTraceInputs {
		return domain.Conflict("input_batch_too_large")
	}
	var raw []byte
	if err := tx.QueryRow(ctx, "SELECT trace_applied FROM runs WHERE id=$1", runID).Scan(&raw); err != nil {
		return err
	}
	state := decodeAppliedTrace(raw)
	state.Revision++
	state.Inputs = ids
	rows, err := tx.Query(
		ctx,
		"SELECT trace_sequence,trace_context FROM run_inputs WHERE run_id=$1 AND id=ANY($2::uuid[]) ORDER BY trace_sequence",
		runID,
		ids,
	)
	if err != nil {
		return err
	}
	for rows.Next() {
		var sequence int64
		var carrier []byte
		if err = rows.Scan(&sequence, &carrier); err != nil {
			rows.Close()
			return err
		}
		if sequence > state.Sequence {
			state.Sequence = sequence
			candidate := telemetry.DecodeTrace(carrier)
			if candidate.SpanContext().IsValid() {
				state.Parent = candidate
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE runs SET trace_applied=$2 WHERE id=$1", runID, document(state))
	return err
}
