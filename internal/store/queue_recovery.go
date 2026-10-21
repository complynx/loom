package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
)

// SweepQueueExhaustion makes exhausted delivery visible without repeating inference or effects.
func (s *Store) SweepQueueExhaustion(ctx context.Context, batch int) (int, error) {
	if batch < 1 {
		batch = 100
	}
	if batch > queueRecoveryBatchLimit {
		batch = queueRecoveryBatchLimit
	}
	recovered := 0
	err := s.tx(ctx, func(tx pgx.Tx) error {
		recovered = 0
		rows, err := tx.Query(ctx, `SELECT id::text FROM runs r
   WHERE state IN ('queued','running') AND (lease_until IS NULL OR lease_until<=clock_timestamp())
   AND EXISTS(SELECT 1 FROM river_job j WHERE j.kind='clx_execute_run' AND j.args->>'run_id'=r.id::text AND j.state IN ('discarded','cancelled')
     AND NOT EXISTS(SELECT 1 FROM river_job newer WHERE newer.kind=j.kind AND newer.args->>'run_id'=r.id::text AND newer.id>j.id))
   AND NOT EXISTS(SELECT 1 FROM river_job j WHERE j.kind='clx_execute_run' AND j.args->>'run_id'=r.id::text
     AND (j.state IN ('available','retryable','scheduled','pending')
       OR (j.state='running' AND COALESCE((j.args->>'delivery_fence')::bigint,-1)>=r.fence)))
   ORDER BY updated_at,id LIMIT $1 FOR UPDATE OF r SKIP LOCKED`, batch)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, id := range ids {
			changed, recoveryErr := recoverExhaustedRun(ctx, tx, id)
			if recoveryErr != nil {
				return recoveryErr
			}
			if changed {
				recovered++
			}
		}
		return nil
	})
	return recovered, err
}

func recoverExhaustedRun(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	var err error
	var live, uncertain bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM river_job j JOIN runs r ON r.id::text=j.args->>'run_id'
 WHERE r.id=$1 AND j.kind='clx_execute_run' AND (j.state IN ('available','retryable','scheduled','pending')
 OR (j.state='running' AND COALESCE((j.args->>'delivery_fence')::bigint,-1)>=r.fence)))`, id).
		Scan(&live); err != nil {
		return false, err
	}
	if live {
		return false, nil
	}
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM steps WHERE run_id=$1 AND state IN ('pending','unknown')) OR EXISTS(SELECT 1 FROM write_operations WHERE run_id=$1 AND state IN ('dispatching','pending','unknown'))`, id).
		Scan(&uncertain); err != nil {
		return false, err
	}
	state, reason := domain.Paused, "queue_attempts_exhausted"
	if uncertain {
		state, reason = domain.Reconciling, "queue_attempts_exhausted_uncertain"
	}
	if _, err = tx.Exec(
		ctx,
		`UPDATE runs SET state=$2,reason=$3,lease_owner='',lease_until=NULL,fence=fence+1 WHERE id=$1`,
		id,
		state,
		reason,
	); err != nil {
		return false, err
	}
	if err = runEvent(
		ctx,
		tx,
		id,
		"run.status",
		map[string]any{eventStatusKey: state, eventReasonKey: reason},
	); err != nil {
		return false, err
	}
	if err = runEvent(
		ctx,
		tx,
		id,
		"run.resumable",
		map[string]string{eventRunIDKey: id, eventReasonKey: reason},
	); err != nil {
		return false, err
	}
	return true, nil
}
