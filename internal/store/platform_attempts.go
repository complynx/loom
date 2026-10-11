package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
)

const platformAttemptUnknown = "unknown"

// SponsoredAttemptCount reports uncertain attempts whose liability belongs to the platform.
func (s *Store) SponsoredAttemptCount(ctx context.Context, rid string) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, "SELECT count(*) FROM platform_provider_attempts WHERE run_id=$1", rid).Scan(&count)
	return count, err
}

// AbsorbUnknownStep releases user funds without claiming that the provider charged them.
// Call before surrendering the worker lease; no tool output is reconstructed.
func (s *Store) AbsorbUnknownStep(
	ctx context.Context,
	rid, owner string,
	fence int64,
	sid string,
	maxAttempts int,
) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		run, err := leaseRun(ctx, tx, rid, owner, fence)
		if err != nil {
			return err
		}
		if run.State != domain.Running {
			return domain.Conflict("run_not_running")
		}
		if run.PauseRequested || run.CancelRequested {
			return domain.Conflict("client_interruption")
		}
		return absorbProviderAttempt(ctx, tx, run, sid, maxAttempts)
	})
}

func absorbProviderAttempt(ctx context.Context, tx pgx.Tx, run domain.Run, sid string, limit int) error {
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM platform_provider_attempts WHERE step_id=$1 AND run_id=$2)", sid, run.ID).
		Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	var count int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM platform_provider_attempts WHERE run_id=$1", run.ID).
		Scan(&count); err != nil {
		return err
	}
	if limit <= 0 || count >= limit {
		return domain.Conflict("platform_retry_limit")
	}
	reserve, err := reconciliationReserve(ctx, tx, run.ID, sid)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(
		ctx,
		`INSERT INTO platform_provider_attempts(step_id,run_id,owner_id,org_id,model_snapshot,client_request_id,reserved_exposure) VALUES($1,$2,$3,$4,$5,$7,$6)`,
		sid,
		run.ID,
		run.OwnerID,
		run.OrgID,
		document(run.Model),
		reserve,
		sid,
	); err != nil {
		return err
	}
	if _, err = tx.Exec(
		ctx,
		"UPDATE billing_accounts SET reserved=reserved-$2 WHERE owner_id=$1",
		run.OwnerID,
		reserve,
	); err != nil {
		return err
	}
	if _, err = tx.Exec(
		ctx,
		"UPDATE runs SET reserved_microcredits=reserved_microcredits-$2 WHERE id=$1",
		run.ID,
		reserve,
	); err != nil {
		return err
	}
	if _, err = tx.Exec(
		ctx,
		`UPDATE steps SET state='completed',result=$2,usage=$3 WHERE id=$1`,
		sid,
		json.RawMessage(`{"ErrorCode":"platform_paid_unknown"}`),
		document(domain.Usage{}),
	); err != nil {
		return err
	}
	if _, err = tx.Exec(
		ctx,
		`INSERT INTO credit_ledger(id,owner_id,run_id,step_id,settlement_key,amount,kind,reason) VALUES($1,$2,$3,$4,$5,$6,'release','platform assumes uncertain provider liability')`,
		id(),
		run.OwnerID,
		run.ID,
		sid,
		"release:"+sid,
		reserve,
	); err != nil {
		return err
	}
	data := map[string]any{"step_id": sid, "reserved_exposure": reserve, "status": platformAttemptUnknown}
	if err = audit(
		ctx,
		tx,
		domain.Principal{Actor: "platform", OrgID: run.OrgID},
		"provider_attempt.platform_paid",
		sid,
		data,
	); err != nil {
		return err
	}
	return runEvent(ctx, tx, run.ID, "provider_attempt.platform_paid", data)
}

// RecoverSponsoredRun repairs expired inference attempts after fresh authorization.
// It never repairs uncertain writes, tools, active leases, or stopped runs.
func (s *Store) RecoverSponsoredRun(ctx context.Context, rid string, maxAttempts int) (bool, error) {
	var repaired bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		run, err := scanRun(tx.QueryRow(ctx, "SELECT "+runColumns+" FROM runs WHERE id=$1 FOR UPDATE", rid))
		if err != nil {
			return err
		}
		var eligible bool
		if err = tx.QueryRow(ctx, `SELECT (state IN('queued','running','reconciling') AND NOT pause_requested AND NOT cancel_requested AND (lease_until IS NULL OR lease_until<=clock_timestamp()) AND NOT EXISTS(SELECT 1 FROM write_operations WHERE run_id=$1 AND state IN('dispatching','pending','unknown')) AND NOT EXISTS(SELECT 1 FROM steps WHERE run_id=$1 AND state IN('pending','unknown') AND kind NOT IN('model','summary','compact'))) FROM runs WHERE id=$1`, rid).
			Scan(&eligible); err != nil {
			return err
		}
		if !eligible {
			return nil
		}
		var pending bool
		if pending, err = absorbPendingAttempts(ctx, tx, run, maxAttempts); err != nil {
			return err
		}
		if !pending {
			return nil
		}
		if _, err = tx.Exec(
			ctx,
			"UPDATE runs SET state='queued',reason='platform_paid_recovery',lease_owner='',lease_until=NULL,fence=fence+1 WHERE id=$1",
			rid,
		); err != nil {
			return err
		}
		if err = runEvent(
			ctx,
			tx,
			rid,
			"run.status",
			map[string]any{eventStatusKey: domain.Queued, eventReasonKey: "platform_paid_recovery"},
		); err != nil {
			return err
		}
		repaired = true
		return nil
	})
	return repaired, err
}

func absorbPendingAttempts(ctx context.Context, tx pgx.Tx, run domain.Run, limit int) (bool, error) {
	rows, err := tx.Query(
		ctx,
		"SELECT id::text FROM steps WHERE run_id=$1 AND state IN('pending','unknown') ORDER BY ordinal",
		run.ID,
	)
	if err != nil {
		return false, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return false, err
	}
	for _, sid := range ids {
		if err = absorbProviderAttempt(ctx, tx, run, sid, limit); err != nil {
			return false, err
		}
	}
	return len(ids) > 0, nil
}
