package store

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
)

// UnresolvedSteps returns receipt identifiers without prompts or credentials.
func (s *Store) UnresolvedSteps(ctx context.Context, rid string) ([]domain.UnresolvedStep, error) {
	rows, e := s.pool.Query(
		ctx,
		"SELECT id::text,kind,reserve FROM steps WHERE run_id=$1 AND state IN('pending','unknown') ORDER BY ordinal",
		rid,
	)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []domain.UnresolvedStep{}
	for rows.Next() {
		var v domain.UnresolvedStep
		if e = rows.Scan(&v.ID, &v.Kind, &v.ReservedMicrocredits); e != nil {
			return nil, e
		}
		v.ClientRequestID = v.ID
		out = append(out, v)
	}
	return out, rows.Err()
}
func receiptCost(usage domain.Usage, m domain.Model) (int64, error) {
	return domain.UsageCost(usage, m)
}

// ReconcileStep settles an evidenced provider receipt; it never retries inference.
func (s *Store) ReconcileStep(
	ctx context.Context,
	p domain.Principal,
	rid, sid, key string,
	in domain.ReconcileStepRequest,
) (domain.Run, error) {
	var r domain.Run
	if !p.Has("operations.reconcile") {
		return r, domain.Forbidden()
	}
	if key == "" || len(key) > 128 || in.Evidence.Reference == "" || len(in.Evidence.Reference) > 2048 ||
		in.Evidence.Reason == "" ||
		len(in.Evidence.Reason) > 1024 ||
		(in.Evidence.Kind != "provider_receipt" && in.Evidence.Kind != "operator_attestation") {
		return r, domain.NewError(http.StatusUnprocessableEntity, "invalid_reconciliation_evidence")
	}
	if len(in.Result) > 524288 || (len(in.Result) > 0 && !json.Valid(in.Result)) {
		return r, domain.NewError(http.StatusUnprocessableEntity, "invalid_receipt_result")
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		return s.reconcileStep(ctx, tx, p, rid, sid, key, in, &r)
	})
	if err != nil {
		return r, err
	}
	r.UnresolvedSteps, err = s.UnresolvedSteps(ctx, rid)
	return r, err
}

func (s *Store) reconcileStep(
	ctx context.Context,
	tx pgx.Tx,
	p domain.Principal,
	rid, sid, key string,
	in domain.ReconcileStepRequest,
	r *domain.Run,
) error {
	var e error
	*r, e = scanRun(
		tx.QueryRow(ctx, "SELECT "+runColumns+" FROM runs WHERE id=$1 AND org_id=$2 FOR UPDATE", rid, p.OrgID),
	)
	if e != nil {
		return e
	}
	sc := scope(p, "reconcile:"+rid+":"+sid)
	existing, e := idem(ctx, tx, sc, key, in)
	if e != nil {
		return e
	}
	if existing != "" {
		return nil
	}
	if err := validateReconciliationRun(*r); err != nil {
		return err
	}
	reserve, e := reconciliationReserve(ctx, tx, rid, sid)
	if e != nil {
		return e
	}
	cost, e := validatedReceiptCost(in.Usage, r.Model, reserve)
	if e != nil {
		return e
	}
	result := in.Result
	if len(result) == 0 {
		result = json.RawMessage(`{"ErrorCode":"receipt_output_unavailable"}`)
	}
	_, e = tx.Exec(
		ctx,
		"UPDATE billing_accounts SET balance=balance-$2,spent=spent+$2,reserved=reserved-$3 WHERE owner_id=$1",
		r.OwnerID,
		cost,
		reserve,
	)
	if e != nil {
		return e
	}
	_, e = tx.Exec(
		ctx,
		"UPDATE steps SET state='completed',result=$2,usage=$3 WHERE id=$1",
		sid,
		result,
		document(in.Usage),
	)
	if e != nil {
		return e
	}
	_, e = tx.Exec(
		ctx,
		"UPDATE runs SET spent_microcredits=spent_microcredits+$2,reserved_microcredits=reserved_microcredits-$3 WHERE id=$1",
		rid,
		cost,
		reserve,
	)
	if e != nil {
		return e
	}
	_, e = tx.Exec(
		ctx,
		"INSERT INTO credit_ledger(id,owner_id,run_id,step_id,settlement_key,amount,kind,reason) VALUES($1,$2,$3,$4,$5,$6,'settle',$7)",
		id(),
		r.OwnerID,
		rid,
		sid,
		"settle:"+sid,
		cost,
		in.Evidence.Reason,
	)
	if e != nil {
		return e
	}
	if e = saveIdem(ctx, tx, sc, key, rid, in); e != nil {
		return e
	}
	if e = audit(ctx, tx, p, "provider_usage.reconcile", sid, in.Evidence); e != nil {
		return e
	}
	if e = runEvent(ctx, tx, rid, "usage.updated", in.Usage); e != nil {
		return e
	}
	if e = pauseReconciledRun(ctx, tx, *r); e != nil {
		return e
	}
	*r, e = scanRun(tx.QueryRow(ctx, "SELECT "+runColumns+" FROM runs WHERE id=$1", rid))
	return e
}
func pauseReconciledRun(ctx context.Context, tx pgx.Tx, r domain.Run) error {
	if r.State != domain.Reconciling {
		return nil
	}
	var pending bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM steps WHERE run_id=$1 AND state IN('pending','unknown'))", r.ID).
		Scan(&pending); err != nil {
		return err
	}
	if pending {
		return nil
	}
	if _, err := tx.Exec(
		ctx,
		"UPDATE runs SET state='paused',reason='usage_reconciled',pause_requested=false,lease_owner='',lease_until=NULL WHERE id=$1",
		r.ID,
	); err != nil {
		return err
	}
	return runEvent(
		ctx,
		tx,
		r.ID,
		"run.resumable",
		map[string]string{eventStatusKey: "paused", eventReasonKey: "usage_reconciled", eventRunIDKey: r.ID},
	)
}

func validateReconciliationRun(r domain.Run) error {
	if r.State != domain.Reconciling && r.State != domain.Cancelled && r.State != domain.Failed {
		return domain.Conflict("run_not_reconciling")
	}
	if r.LeaseUntil != nil && r.LeaseUntil.After(time.Now()) {
		return domain.Conflict("lease_busy")
	}
	return nil
}

func validatedReceiptCost(usage domain.Usage, model domain.Model, reserve int64) (int64, error) {
	cost, err := receiptCost(usage, model)
	if err != nil {
		return 0, err
	}
	if cost != usage.Microcredits {
		return 0, domain.NewError(http.StatusUnprocessableEntity, "receipt_tariff_mismatch")
	}
	if cost > reserve {
		return 0, domain.Conflict("usage_exceeds_reservation")
	}
	return cost, nil
}

func reconciliationReserve(ctx context.Context, tx pgx.Tx, rid, sid string) (int64, error) {
	var state, kind string
	var reserve int64
	if err := tx.QueryRow(ctx, "SELECT state,kind,reserve FROM steps WHERE id=$1 AND run_id=$2 FOR UPDATE", sid, rid).
		Scan(&state, &kind, &reserve); err != nil {
		return 0, err
	}
	if (state != "unknown" && state != stepPending) || (kind != "model" && kind != "summary" && kind != "compact") {
		return 0, domain.Conflict("step_not_reconcilable")
	}
	return reserve, nil
}
