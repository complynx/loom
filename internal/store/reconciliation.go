package store

import (
	"context"
	"encoding/json"
	"github.com/complynx/clx-scripter/internal/domain"
	"github.com/jackc/pgx/v5"
	"time"
)

// UnresolvedSteps returns receipt identifiers without prompts or credentials.
func (s *Store) UnresolvedSteps(ctx context.Context, rid string) ([]domain.UnresolvedStep, error) {
	rows, e := s.pool.Query(ctx, "SELECT id::text,kind,reserve FROM steps WHERE run_id=$1 AND state IN('pending','unknown') ORDER BY ordinal", rid)
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
func (s *Store) ReconcileStep(ctx context.Context, p domain.Principal, rid, sid, key string, in domain.ReconcileStepRequest) (domain.Run, error) {
	var r domain.Run
	if !p.Has("operations.reconcile") {
		return r, domain.Forbidden()
	}
	if key == "" || len(key) > 128 || in.Evidence.Reference == "" || len(in.Evidence.Reference) > 2048 || in.Evidence.Reason == "" || len(in.Evidence.Reason) > 1024 || (in.Evidence.Kind != "provider_receipt" && in.Evidence.Kind != "operator_attestation") {
		return r, domain.NewError(422, "invalid_reconciliation_evidence")
	}
	if len(in.Result) > 524288 || (len(in.Result) > 0 && !json.Valid(in.Result)) {
		return r, domain.NewError(422, "invalid_receipt_result")
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var e error
		r, e = scanRun(tx.QueryRow(ctx, "SELECT "+runColumns+" FROM runs WHERE id=$1 AND org_id=$2 FOR UPDATE", rid, p.OrgID))
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
		if r.State != domain.Reconciling && r.State != domain.Cancelled && r.State != domain.Failed {
			return domain.Conflict("run_not_reconciling")
		}
		if r.LeaseUntil != nil && r.LeaseUntil.After(time.Now()) {
			return domain.Conflict("lease_busy")
		}
		var state, kind string
		var reserve int64
		e = tx.QueryRow(ctx, "SELECT state,kind,reserve FROM steps WHERE id=$1 AND run_id=$2 FOR UPDATE", sid, rid).Scan(&state, &kind, &reserve)
		if e != nil {
			return e
		}
		if (state != "unknown" && state != "pending") || (kind != "model" && kind != "summary") {
			return domain.Conflict("step_not_reconcilable")
		}
		cost, e := receiptCost(in.Usage, r.Model)
		if e != nil {
			return e
		}
		if cost != in.Usage.Microcredits {
			return domain.NewError(422, "receipt_tariff_mismatch")
		}
		if cost > reserve {
			return domain.Conflict("usage_exceeds_reservation")
		}
		result := in.Result
		if len(result) == 0 {
			result = json.RawMessage(`{"ErrorCode":"receipt_output_unavailable"}`)
		}
		_, e = tx.Exec(ctx, "UPDATE billing_accounts SET balance=balance-$2,spent=spent+$2,reserved=reserved-$3 WHERE owner_id=$1", r.OwnerID, cost, reserve)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, "UPDATE steps SET state='completed',result=$2,usage=$3 WHERE id=$1", sid, result, document(in.Usage))
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, "UPDATE runs SET spent_microcredits=spent_microcredits+$2,reserved_microcredits=reserved_microcredits-$3 WHERE id=$1", rid, cost, reserve)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, "INSERT INTO credit_ledger(id,owner_id,run_id,step_id,settlement_key,amount,kind,reason) VALUES($1,$2,$3,$4,$5,$6,'settle',$7)", id(), r.OwnerID, rid, sid, "settle:"+sid, cost, in.Evidence.Reason)
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
		if r.State == domain.Reconciling {
			var pending bool
			if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM steps WHERE run_id=$1 AND state IN('pending','unknown'))", rid).Scan(&pending); e != nil {
				return e
			}
			if !pending {
				_, e = tx.Exec(ctx, "UPDATE runs SET state='paused',reason='usage_reconciled',pause_requested=false,lease_owner='',lease_until=NULL WHERE id=$1", rid)
				if e != nil {
					return e
				}
				if e = runEvent(ctx, tx, rid, "run.resumable", map[string]string{"status": "paused", "reason": "usage_reconciled", "run_id": rid}); e != nil {
					return e
				}
			}
		}
		r, e = scanRun(tx.QueryRow(ctx, "SELECT "+runColumns+" FROM runs WHERE id=$1", rid))
		return e
	})
	if err != nil {
		return r, err
	}
	r.UnresolvedSteps, err = s.UnresolvedSteps(ctx, rid)
	return r, err
}
