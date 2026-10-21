package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
)

// SweepOrphanedRuns fences expired executors and commits replacement delivery
// for the same run. Authorization and uncertain-work recovery stay in the engine.
func (s *Store) SweepOrphanedRuns(ctx context.Context, batch int) (int, error) {
	if s.enqueue == nil {
		return 0, nil
	}
	if batch < 1 || batch > queueRecoveryBatchLimit {
		batch = 100
	}
	recovered := 0
	err := s.tx(ctx, func(tx pgx.Tx) error {
		recovered = 0
		rows, err := tx.Query(ctx, `SELECT r.id::text FROM runs r
 WHERE r.state='running' AND (r.lease_until IS NULL OR r.lease_until<=clock_timestamp())
 AND NOT r.pause_requested AND NOT r.cancel_requested
 AND NOT EXISTS(SELECT 1 FROM river_job j WHERE j.kind='clx_execute_run' AND j.args->>'run_id'=r.id::text
   AND (j.state IN ('available','retryable','scheduled','pending')
     OR (j.state='running' AND COALESCE((j.args->>'delivery_fence')::bigint,-1)>=r.fence)))
 ORDER BY r.updated_at,r.id LIMIT $1 FOR UPDATE OF r SKIP LOCKED`, batch)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, id := range ids {
			if _, err = tx.Exec(
				ctx,
				`UPDATE runs SET state='queued',reason='orphaned_delivery',fence=fence+1,lease_owner='',lease_until=NULL WHERE id=$1`,
				id,
			); err != nil {
				return err
			}
			if err = runEvent(
				ctx,
				tx,
				id,
				"run.status",
				map[string]any{eventStatusKey: domain.Queued, eventReasonKey: "orphaned_delivery"},
			); err != nil {
				return err
			}
			if err = s.enqueue(ctx, tx, id); err != nil {
				return err
			}
			recovered++
		}
		return nil
	})
	return recovered, err
}
