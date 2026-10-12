package store

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
)

// PrepareRecovery fences expired uncertain work without dispatch or settlement.
// The caller must hold all execution workers stopped; the HTTP route requires recovery mode.
func (s *Store) PrepareRecovery(
	ctx context.Context,
	p domain.Principal,
	rid, key string,
	in domain.RecoveryPrepareRequest,
) (domain.Run, error) {
	var r domain.Run
	if !p.Has("operations.reconcile") {
		return r, domain.Forbidden()
	}
	if key == "" || len(key) > 128 || in.ExpectedFence < 1 || in.Evidence.Kind != "operator_attestation" ||
		in.Evidence.Reference == "" ||
		len(in.Evidence.Reference) > 2048 ||
		in.Evidence.Reason == "" ||
		len(in.Evidence.Reason) > 1024 {
		return r, domain.NewError(http.StatusUnprocessableEntity, "invalid_recovery_evidence")
	}
	err := s.tx(ctx, func(tx pgx.Tx) error { return s.prepareRecovery(ctx, tx, p, rid, key, in, &r) })
	if err != nil {
		return r, err
	}
	r.UnresolvedSteps, err = s.UnresolvedSteps(ctx, rid)
	return r, err
}

func (s *Store) prepareRecovery(
	ctx context.Context,
	tx pgx.Tx,
	p domain.Principal,
	rid, key string,
	in domain.RecoveryPrepareRequest,
	r *domain.Run,
) error {
	var err error
	*r, err = scanRun(
		tx.QueryRow(ctx, "SELECT "+runColumns+" FROM runs WHERE id=$1 AND org_id=$2 FOR UPDATE", rid, p.OrgID),
	)
	if err != nil {
		return err
	}
	sc := scope(p, "recovery.prepare:"+rid)
	previous, err := idem(ctx, tx, sc, key, in)
	if err != nil {
		return err
	}
	if previous != "" {
		return nil
	}
	if r.Fence != in.ExpectedFence {
		return domain.Conflict("recovery_fence_changed")
	}
	if r.State != domain.Running {
		return domain.Conflict("run_not_running")
	}
	var active, uncertain bool
	err = tx.QueryRow(ctx, `SELECT lease_until IS NOT NULL AND lease_until>clock_timestamp(),
 EXISTS(SELECT 1 FROM steps WHERE run_id=$1 AND state IN('pending','unknown')) OR
 EXISTS(SELECT 1 FROM write_operations WHERE run_id=$1 AND state IN('dispatching','pending','unknown')) OR
 EXISTS(SELECT 1 FROM platform_provider_attempts WHERE run_id=$1 AND status='unknown')
 FROM runs WHERE id=$1`, rid).Scan(&active, &uncertain)
	if err != nil {
		return err
	}
	if active {
		return domain.Conflict("lease_busy")
	}
	if !uncertain {
		return domain.Conflict("run_not_uncertain")
	}
	_, err = tx.Exec(
		ctx,
		"UPDATE runs SET state='reconciling',reason='recovery_prepared',lease_owner='',lease_until=NULL,fence=fence+1 WHERE id=$1",
		rid,
	)
	if err != nil {
		return err
	}
	if err = saveIdem(ctx, tx, sc, key, rid, in); err != nil {
		return err
	}
	if err = audit(ctx, tx, p, "run.recovery.prepare", rid, in); err != nil {
		return err
	}
	if err = runEvent(
		ctx,
		tx,
		rid,
		"run.status",
		map[string]any{eventStatusKey: domain.Reconciling, eventReasonKey: "recovery_prepared"},
	); err != nil {
		return err
	}
	*r, err = scanRun(tx.QueryRow(ctx, "SELECT "+runColumns+" FROM runs WHERE id=$1", rid))
	return err
}
