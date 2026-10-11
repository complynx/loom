package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/complynx/clx-scripter/internal/domain"
)

func (s *Store) ProviderRetry(ctx context.Context, rid string) (domain.ProviderRetry, error) {
	var retry domain.ProviderRetry
	err := s.pool.QueryRow(ctx, `SELECT attempts,next_attempt_at,deadline,code FROM provider_retries WHERE run_id=$1`, rid).
		Scan(&retry.Attempts, &retry.NextAttemptAt, &retry.Deadline, &retry.Code)
	if errors.Is(err, pgx.ErrNoRows) {
		return retry, nil
	}
	return retry, err
}

// DeferProviderRetry settles a known rejection and releases the lease in one
// transaction. River may crash before snoozing; the stored schedule still gates dispatch.
func (s *Store) DeferProviderRetry(
	ctx context.Context,
	rid, owner string,
	fence int64,
	sid string,
	retry domain.ProviderRetry,
	limit int,
) (domain.ProviderRetry, error) {
	err := s.tx(ctx, func(tx pgx.Tx) error {
		run, err := leaseRun(ctx, tx, rid, owner, fence)
		if err != nil {
			return err
		}
		result, _ := json.Marshal(map[string]string{"ErrorCode": "provider_retry"})
		if err = s.completeStep(ctx, tx, rid, owner, fence, sid, result, domain.Usage{}); err != nil {
			return err
		}
		var previous domain.ProviderRetry
		err = tx.QueryRow(ctx, `SELECT attempts,deadline FROM provider_retries WHERE run_id=$1`, rid).
			Scan(&previous.Attempts, &previous.Deadline)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		retry.Attempts += previous.Attempts
		if !previous.Deadline.IsZero() {
			retry.Deadline = previous.Deadline
		}
		if _, err = tx.Exec(
			ctx,
			`INSERT INTO provider_retries(run_id,attempts,next_attempt_at,deadline,code) VALUES($1,$2,$3,$4,$5) ON CONFLICT(run_id) DO UPDATE SET attempts=excluded.attempts,next_attempt_at=excluded.next_attempt_at,code=excluded.code`,
			rid,
			retry.Attempts,
			retry.NextAttemptAt,
			retry.Deadline,
			retry.Code,
		); err != nil {
			return err
		}
		if run.CancelRequested {
			return s.transition(ctx, tx, rid, owner, fence, domain.Cancelled, "client_cancel", "")
		}
		if run.PauseRequested {
			return s.transition(ctx, tx, rid, owner, fence, domain.Paused, "client_pause", "")
		}
		if retry.Attempts >= limit || !retry.NextAttemptAt.Before(retry.Deadline) {
			return s.transition(ctx, tx, rid, owner, fence, domain.Paused, "provider_retry_limit", "")
		}
		if _, err = tx.Exec(
			ctx,
			`UPDATE runs SET state='queued',reason='provider_retry',lease_owner='',lease_until=NULL,fence=fence+1 WHERE id=$1`,
			rid,
		); err != nil {
			return err
		}
		return runEvent(
			ctx,
			tx,
			rid,
			"run.status",
			map[string]any{
				eventStatusKey:    domain.Queued,
				eventReasonKey:    "provider_retry",
				"next_attempt_at": retry.NextAttemptAt,
				"attempts":        retry.Attempts,
				"deadline":        retry.Deadline,
				"code":            retry.Code,
			},
		)
	})
	return retry, err
}

// A verified success closes its retry episode in the same settlement transaction.
func closeProviderRetry(ctx context.Context, tx pgx.Tx, rid, sid string) error {
	var retry domain.ProviderRetry
	err := tx.QueryRow(
		ctx,
		"DELETE FROM provider_retries WHERE run_id=$1 AND EXISTS(SELECT 1 FROM steps WHERE id=$2 AND run_id=$1 AND kind IN ('model','summary','compact') AND state='completed' AND COALESCE(result->>'ErrorCode','')='' AND COALESCE(result->>'ID','')<>'') RETURNING attempts,next_attempt_at,deadline,code",
		rid,
		sid,
	).Scan(&retry.Attempts, &retry.NextAttemptAt, &retry.Deadline, &retry.Code)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return runEvent(ctx, tx, rid, "provider.retry.recovered", retry)
}

// Explicit resume restarts an exhausted episode only after the server wait is due.
func resetProviderRetryForResume(ctx context.Context, tx pgx.Tx, run domain.Run) error {
	if run.State != domain.Paused {
		return nil
	}
	var retry domain.ProviderRetry
	err := tx.QueryRow(ctx, "SELECT attempts,next_attempt_at,deadline,code FROM provider_retries WHERE run_id=$1", run.ID).
		Scan(&retry.Attempts, &retry.NextAttemptAt, &retry.Deadline, &retry.Code)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if retry.NextAttemptAt.After(time.Now()) {
		return domain.Conflict("retry_not_due")
	}
	if run.Reason != "provider_retry_limit" && retry.Deadline.After(time.Now()) {
		return nil
	}
	if _, err = tx.Exec(ctx, "DELETE FROM provider_retries WHERE run_id=$1", run.ID); err != nil {
		return err
	}
	return runEvent(ctx, tx, run.ID, "provider.retry.restarted", retry)
}

// The run lock makes this schedule check atomic with claiming the next lease.
func checkProviderRetryClaim(ctx context.Context, tx pgx.Tx, run domain.Run) error {
	if run.PauseRequested || run.CancelRequested {
		return nil
	}
	var next, deadline time.Time
	err := tx.QueryRow(ctx, "SELECT next_attempt_at,deadline FROM provider_retries WHERE run_id=$1", run.ID).
		Scan(&next, &deadline)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	now := time.Now()
	if next.After(now) && deadline.After(now) {
		if deadline.Before(next) {
			next = deadline
		}
		return &domain.ProviderRetryWaitError{Until: next}
	}
	return nil
}
