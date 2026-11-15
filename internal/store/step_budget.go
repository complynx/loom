package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
)

// Record the completed step count under the run lock, after validation and
// idempotency lookup. Return true when the input should wake a step_limit pause.
func renewStepBudget(ctx context.Context, tx pgx.Tx, r *domain.Run) (bool, error) {
	var err error
	r.StepBudgetStart, err = completedStepCount(ctx, tx, r.ID)
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, "UPDATE runs SET doc=$2 WHERE id=$1", r.ID, document(r)); err != nil {
		return false, err
	}
	return stepLimitWakes(ctx, tx, *r)
}

// stepLimitWakes reports whether a new input wakes a run paused at its step limit.
// A pause request, a cancellation or an unknown outcome holds the run.
func stepLimitWakes(ctx context.Context, tx pgx.Tx, r domain.Run) (bool, error) {
	if r.State != domain.Paused || r.Reason != "step_limit" || r.PauseRequested || r.CancelRequested {
		return false, nil
	}
	uncertain, err := runUncertainty(ctx, tx, r.ID)
	return !uncertain, err
}

// A new input can commit after the executor reads the run. Recheck under the
// run lock so a stale limit decision cannot strand that input.
func recheckStepLimit(ctx context.Context, tx pgx.Tx, r domain.Run, state *domain.RunState, reason *string) error {
	if *state != domain.Paused || *reason != "step_limit" {
		return nil
	}
	steps, err := completedStepCount(ctx, tx, r.ID)
	if err != nil {
		return err
	}
	if !r.StepLimitReached(steps) {
		*state, *reason = domain.Queued, ""
	}
	return nil
}

func completedStepCount(ctx context.Context, tx pgx.Tx, rid string) (int, error) {
	var steps int
	err := tx.QueryRow(ctx, "SELECT count(*) FROM steps WHERE run_id=$1 AND state='completed'", rid).Scan(&steps)
	return steps, err
}
