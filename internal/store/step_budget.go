package store

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
)

// Do not charge an obsolete step-limit note if input renewed the work before
// admission. The run lock makes this check atomic with insertion of the note.
func checkLimitNoteAdmission(ctx context.Context, tx pgx.Tx, r domain.Run, kind string, input json.RawMessage) error {
	if kind != domain.LimitNoteStepKind {
		return nil
	}
	var note struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(input, &note); err != nil {
		return domain.NewError(http.StatusUnprocessableEntity, "invalid_step")
	}
	if note.Reason != stepLimitReason {
		return nil
	}
	completed, err := completedStepCount(ctx, tx, r.ID)
	if err != nil {
		return err
	}
	if !r.StepLimitReached(completed) {
		return domain.Conflict("step_budget_renewed")
	}
	return nil
}

// Record the completed step count under the run lock, after validation and
// idempotency lookup. Return true when the input should wake a step_limit pause.
func renewStepBudget(ctx context.Context, tx pgx.Tx, r *domain.Run) (bool, error) {
	var err error
	// A note already in flight belongs to the preceding interaction. Its later
	// completion must not consume the allowance renewed by this input.
	err = tx.QueryRow(ctx, "SELECT count(*) FROM steps WHERE run_id=$1 AND (state='completed' OR (kind=$2 AND state IN('pending','unknown')))", r.ID, domain.LimitNoteStepKind).
		Scan(&r.StepBudgetStart)
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, "UPDATE runs SET doc=$2 WHERE id=$1", r.ID, document(r)); err != nil {
		return false, err
	}
	return inputWakes(ctx, tx, *r)
}

// inputWakes reports whether a new input wakes a paused run. It wakes a run
// paused at its step limit, and a run paused for an empty wallet, which then
// tries its step again. A pause request, a cancellation or an unknown outcome
// holds the run.
func inputWakes(ctx context.Context, tx pgx.Tx, r domain.Run) (bool, error) {
	if r.State != domain.Paused || r.PauseRequested || r.CancelRequested ||
		(r.Reason != stepLimitReason && r.Reason != creditsExhaustedReason) {
		return false, nil
	}
	uncertain, err := runUncertainty(ctx, tx, r.ID)
	return !uncertain, err
}

// A new input can commit after the executor reads the run. Recheck under the
// run lock so a stale limit decision cannot strand that input.
func recheckStepLimit(ctx context.Context, tx pgx.Tx, r domain.Run, state *domain.RunState, reason *string) error {
	if *state != domain.Paused || *reason != stepLimitReason {
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
