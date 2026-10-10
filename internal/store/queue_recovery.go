package store

import (
	"context"

	"github.com/complynx/clx-scripter/internal/domain"
	"github.com/jackc/pgx/v5"
)

// SweepQueueExhaustion makes exhausted delivery visible without repeating inference or effects.
func (s *Store) SweepQueueExhaustion(ctx context.Context, batch int) (int, error) {
	if batch < 1 {
		batch = 100
	}
	if batch > 500 {
		batch = 500
	}
	recovered := 0
	err := s.tx(ctx, func(tx pgx.Tx) error {
		recovered = 0
		rows, err := tx.Query(ctx, `SELECT id::text FROM runs r
   WHERE state IN ('queued','running') AND (lease_until IS NULL OR lease_until<=clock_timestamp())
   AND EXISTS(SELECT 1 FROM river_job j WHERE j.kind='clx_execute_run' AND j.args->>'run_id'=r.id::text AND j.state IN ('discarded','cancelled'))
   AND NOT EXISTS(SELECT 1 FROM river_job j WHERE j.kind='clx_execute_run' AND j.args->>'run_id'=r.id::text AND j.state IN ('available','retryable','scheduled','running','pending'))
   ORDER BY updated_at,id LIMIT $1 FOR UPDATE OF r SKIP LOCKED`, batch)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, id := range ids {
			var live, uncertain bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM river_job WHERE kind='clx_execute_run' AND args->>'run_id'=$1 AND state IN ('available','retryable','scheduled','running','pending'))`, id).Scan(&live); err != nil {
				return err
			}
			if live {
				continue
			}
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM steps WHERE run_id=$1 AND state IN ('pending','unknown')) OR EXISTS(SELECT 1 FROM write_operations WHERE run_id=$1 AND state IN ('dispatching','pending','unknown'))`, id).Scan(&uncertain); err != nil {
				return err
			}
			state, reason := domain.Paused, "queue_attempts_exhausted"
			if uncertain {
				state, reason = domain.Reconciling, "queue_attempts_exhausted_uncertain"
			}
			if _, err = tx.Exec(ctx, `UPDATE runs SET state=$2,reason=$3,lease_owner='',lease_until=NULL,fence=fence+1 WHERE id=$1`, id, state, reason); err != nil {
				return err
			}
			if err = runEvent(ctx, tx, id, "run.status", map[string]any{"status": state, "reason": reason}); err != nil {
				return err
			}
			if err = runEvent(ctx, tx, id, "run.resumable", map[string]string{"run_id": id, "reason": reason}); err != nil {
				return err
			}
			recovered++
		}
		return nil
	})
	return recovered, err
}
