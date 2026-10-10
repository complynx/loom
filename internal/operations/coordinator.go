package operations

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Coordinator struct {
	Pool        *pgxpool.Pool
	ApprovalTTL time.Duration
}

func canonical(raw json.RawMessage) (json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value any
	if err := d.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := d.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("arguments must contain one JSON value")
	}
	return json.Marshal(value)
}

func digest(value any) string {
	b, _ := json.Marshal(value)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func seal(op Operation) string {
	return digest(struct {
		Request     Request
		Preparation Preparation
	}{op.Request, op.Preparation})
}

// Prepare deduplicates the stable call identity. A changed argument or authority
// under the same identity is rejected; it does not silently replace an intent.
func (c *Coordinator) Prepare(ctx context.Context, req Request, backend Backend) (Operation, error) {
	if req.CallID == "" || req.Module == "" || req.Method == "" || req.Binding.UserID == "" || req.Binding.Subject == "" || req.Binding.ContractHash == "" {
		return Operation{}, ErrBinding
	}
	if req.Mode != "none" && req.Mode != "service_id" && req.Mode != "exact_parameters" {
		return Operation{}, ErrBinding
	}
	args, err := canonical(req.Arguments)
	if err != nil {
		return Operation{}, err
	}
	req.Arguments = args
	id, err := newID()
	if err != nil {
		return Operation{}, err
	}
	op := Operation{Request: req, ID: id, State: "preparing", ArgumentHash: digest(args), CreatedAt: time.Now().UTC()}
	doc, err := json.Marshal(op)
	if err != nil {
		return Operation{}, err
	}
	// Run authority is checked before preparing even though preparation cannot
	// perform a write. Dispatch later validates the live fenced lease again.
	tag, err := c.Pool.Exec(ctx, `INSERT INTO write_operations(id,run_id,call_id,state,doc)
	 SELECT $1,r.id,$3,'preparing',$4 FROM runs r JOIN users u ON u.id=r.owner_id
	 WHERE r.id=$2 AND r.owner_id=$5 AND r.actor_id=$6 AND r.org_id=$7 AND u.issuer=$8 AND u.subject=$9
	 ON CONFLICT(run_id,call_id) DO NOTHING`, id, req.RunID, req.CallID, doc, req.Binding.UserID, req.Binding.Actor, req.Binding.OrgID, req.Binding.Issuer, req.Binding.Subject)
	if err != nil {
		return Operation{}, err
	}
	if tag.RowsAffected() == 0 {
		var raw []byte
		if err = c.Pool.QueryRow(ctx, `SELECT doc FROM write_operations WHERE run_id=$1 AND call_id=$2`, req.RunID, req.CallID).Scan(&raw); err != nil {
			return Operation{}, err
		}
		var prior Operation
		if err = json.Unmarshal(raw, &prior); err != nil {
			return Operation{}, err
		}
		if digest(prior.Request) != digest(req) {
			return Operation{}, ErrBinding
		}
		if prior.State == "preparing" {
			return prior, ErrState
		}
		return prior, nil
	}
	prep, err := backend.Prepare(ctx, op)
	if err != nil {
		op.State = "failed"
		_ = c.saveDetached(ctx, op, "preparing")
		return op, err
	}
	if req.Mode == "service_id" && prep.Token == "" {
		op.State = "failed"
		_ = c.saveDetached(ctx, op, "preparing")
		return op, ErrBinding
	}
	if len(prep.Arguments) == 0 {
		prep.Arguments = op.Arguments
	}
	prep.Arguments, err = canonical(prep.Arguments)
	if err != nil {
		op.State = "failed"
		_ = c.saveDetached(ctx, op, "preparing")
		return op, err
	}
	if len(prep.Preconditions) > 0 {
		prep.Preconditions, err = canonical(prep.Preconditions)
		if err != nil {
			op.State = "failed"
			_ = c.saveDetached(ctx, op, "preparing")
			return op, err
		}
	}
	ttl := c.ApprovalTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	maxExpiry := time.Now().UTC().Add(ttl)
	if prep.ExpiresAt.IsZero() || prep.ExpiresAt.After(maxExpiry) {
		prep.ExpiresAt = maxExpiry
	}
	if !prep.ExpiresAt.After(time.Now()) {
		op.State = "failed"
		_ = c.saveDetached(ctx, op, "preparing")
		return op, ErrExpired
	}
	op.Preparation = prep
	op.State = "prepared"
	if req.Mode == "none" {
		op.State = "approved"
	}
	op.BindingHash = seal(op)
	if err = c.save(ctx, op, "preparing"); err != nil {
		return Operation{}, err
	}
	return op, nil
}

func (c *Coordinator) Get(ctx context.Context, id string) (Operation, error) {
	var raw []byte
	err := c.Pool.QueryRow(ctx, `SELECT doc FROM write_operations WHERE id=$1`, id).Scan(&raw)
	if err != nil {
		return Operation{}, err
	}
	var op Operation
	err = json.Unmarshal(raw, &op)
	return op, err
}

func (c *Coordinator) save(ctx context.Context, op Operation, prior string) error {
	doc, err := json.Marshal(op)
	if err != nil {
		return err
	}
	tag, err := c.Pool.Exec(ctx, `UPDATE write_operations SET state=$2,doc=$3,updated_at=clock_timestamp() WHERE id=$1 AND state=$4`, op.ID, op.State, doc, prior)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrState
	}
	return err
}

// Once a write may have happened, client cancellation must not discard its
// outcome. The bounded detached context only persists evidence; it sends no RPC.
func (c *Coordinator) saveDetached(parent context.Context, op Operation, prior string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), time.Second)
	defer cancel()
	return c.save(ctx, op, prior)
}

func (c *Coordinator) Approve(ctx context.Context, id string, a Approval) (Operation, error) {
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return Operation{}, err
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	op, err := c.ApproveTx(ctx, tx, id, a)
	if err != nil {
		return op, err
	}
	err = tx.Commit(ctx)
	return op, err
}

// ApproveTx participates in the run owner's approval-and-wakeup transaction.
// The caller must lock the run first, so approval and dispatch share lock order.
func (c *Coordinator) ApproveTx(ctx context.Context, tx pgx.Tx, id string, a Approval) (Operation, error) {
	op, err := locked(ctx, tx, id)
	if err != nil {
		return Operation{}, err
	}
	if op.Binding != a.Binding || op.BindingHash != a.BindingHash || op.BindingHash != seal(op) {
		return Operation{}, ErrBinding
	}
	if (op.State == "approved" && a.Approve) || (op.State == "rejected" && !a.Approve) {
		return op, nil
	}
	if op.State != "prepared" {
		return Operation{}, ErrState
	}
	if !op.Preparation.ExpiresAt.After(time.Now()) {
		return Operation{}, ErrExpired
	}
	op.State = "rejected"
	if a.Approve {
		op.State = "approved"
	}
	if err = updateTx(ctx, tx, op); err != nil {
		return Operation{}, err
	}
	return op, nil
}

func locked(ctx context.Context, tx pgx.Tx, id string) (Operation, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT doc FROM write_operations WHERE id=$1 FOR UPDATE`, id).Scan(&raw)
	if err != nil {
		return Operation{}, err
	}
	var op Operation
	err = json.Unmarshal(raw, &op)
	return op, err
}
func updateTx(ctx context.Context, tx pgx.Tx, op Operation) error {
	raw, err := json.Marshal(op)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE write_operations SET state=$2,doc=$3,updated_at=clock_timestamp() WHERE id=$1`, op.ID, op.State, raw)
	return err
}

// Dispatch consumes approval and writes dispatching intent in one short fenced
// transaction. Any later call returns the saved state instead of resending.
func (c *Coordinator) Dispatch(ctx context.Context, id string, lease Lease, authorize Authorize, backend Backend) (Operation, error) {
	op, err := c.Get(ctx, id)
	if err != nil {
		return Operation{}, err
	}
	if authorize == nil {
		return op, ErrBinding
	}
	if err = authorize(ctx, op); err != nil {
		return op, err
	}
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return op, err
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	var valid bool
	err = tx.QueryRow(ctx, `SELECT state='running' AND lease_owner=$2 AND fence=$3 AND lease_until>clock_timestamp() AND NOT pause_requested AND NOT cancel_requested FROM runs WHERE id=$1 AND owner_id=$4 AND actor_id=$5 AND org_id=$6 FOR UPDATE`, op.RunID, lease.Owner, lease.Fence, op.Binding.UserID, op.Binding.Actor, op.Binding.OrgID).Scan(&valid)
	if err != nil {
		return op, err
	}
	if !valid {
		return op, ErrLease
	}
	op, err = locked(ctx, tx, id)
	if err != nil {
		return op, err
	}
	if op.State == "prepared" {
		return op, ErrApprovalRequired
	}
	if op.State != "approved" {
		return op, nil
	}
	if op.BindingHash != seal(op) {
		return op, ErrBinding
	}
	if !op.Preparation.ExpiresAt.After(time.Now()) {
		return op, ErrExpired
	}
	op.State = "dispatching"
	if err = updateTx(ctx, tx, op); err != nil {
		return op, err
	}
	if err = tx.Commit(ctx); err != nil {
		return op, err
	}
	// Auth may have changed while waiting for the database lock. Check again
	// after the intent commit, immediately before the remote side effect.
	if err = authorize(ctx, op); err != nil {
		op.State = "failed"
		if persistErr := c.saveDetached(ctx, op, "dispatching"); persistErr != nil {
			return op, persistErr
		}
		return op, err
	}
	if !op.Preparation.ExpiresAt.After(time.Now()) {
		op.State = "failed"
		if persistErr := c.saveDetached(ctx, op, "dispatching"); persistErr != nil {
			return op, persistErr
		}
		return op, ErrExpired
	}
	// Authorization may perform network IO. Recheck fencing and cancellation
	// afterwards so that an expired lease cannot initiate the remote request.
	var canDispatch bool
	err = c.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE id=$1 AND state='running' AND lease_owner=$2 AND fence=$3 AND lease_until>clock_timestamp() AND NOT pause_requested AND NOT cancel_requested)`, op.RunID, lease.Owner, lease.Fence).Scan(&canDispatch)
	if err != nil || !canDispatch {
		op.State = "failed"
		if persistErr := c.saveDetached(ctx, op, "dispatching"); persistErr != nil {
			return op, persistErr
		}
		if err != nil {
			return op, err
		}
		return op, ErrLease
	}
	result, callErr := backend.Execute(ctx, op)
	if callErr != nil {
		op.State = "unknown"
	} else {
		op.State = outcome(result.State)
		op.Result = result.Output
	}
	persistErr := c.saveDetached(ctx, op, "dispatching")
	if persistErr != nil {
		return op, persistErr
	}
	return op, callErr
}

func outcome(state string) string {
	switch state {
	case "succeeded", "failed", "pending":
		return state
	default:
		return "unknown"
	}
}

// Reconcile issues only a status lookup. Even not_found remains unknown: absence
// alone cannot prove a delayed request will never execute.
func (c *Coordinator) Reconcile(ctx context.Context, id string, backend Backend) (Operation, error) {
	op, err := c.Get(ctx, id)
	if err != nil {
		return op, err
	}
	if op.State != "unknown" && op.State != "pending" && op.State != "dispatching" {
		return op, nil
	}
	prior := op.State
	result, err := backend.Status(ctx, op)
	if err != nil {
		return op, err
	}
	op.State = outcome(result.State)
	op.Result = result.Output
	err = c.save(ctx, op, prior)
	return op, err
}

// FindByCall reads an existing intent without manufacturing an operation for a
// superseded tool call. Absence permits the engine to record a skipped call.
func (c *Coordinator) FindByCall(ctx context.Context, runID, callID string) (Operation, bool, error) {
	var raw []byte
	err := c.Pool.QueryRow(ctx, `SELECT doc FROM write_operations WHERE run_id=$1 AND call_id=$2`, runID, callID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return Operation{}, false, nil
	}
	if err != nil {
		return Operation{}, false, err
	}
	var op Operation
	if err = json.Unmarshal(raw, &op); err != nil {
		return Operation{}, false, err
	}
	return op, true, nil
}

// CancelUndispatched records steering only while the intent is proven not to
// have crossed the dispatch boundary. It never cancels or retries a remote
// action. A false result requires the caller to retain the actual saved outcome
// or reconcile uncertain states through Status.
func (c *Coordinator) CancelUndispatched(ctx context.Context, id string, lease Lease, binding Binding) (Operation, bool, error) {
	op, err := c.Get(ctx, id)
	if err != nil {
		return Operation{}, false, err
	}
	if op.Binding != binding {
		return op, false, ErrBinding
	}
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return op, false, err
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	var valid bool
	err = tx.QueryRow(ctx, `SELECT COALESCE(r.state='running' AND r.lease_owner=$2 AND r.fence=$3 AND r.lease_until>clock_timestamp() AND NOT r.pause_requested AND NOT r.cancel_requested,false) FROM runs r JOIN users u ON u.id=r.owner_id WHERE r.id=$1 AND r.owner_id=$4 AND r.actor_id=$5 AND r.org_id=$6 AND u.issuer=$7 AND u.subject=$8 FOR UPDATE OF r`, op.RunID, lease.Owner, lease.Fence, binding.UserID, binding.Actor, binding.OrgID, binding.Issuer, binding.Subject).Scan(&valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return op, false, ErrLease
	}
	if err != nil {
		return op, false, err
	}
	if !valid {
		return op, false, ErrLease
	}
	op, err = locked(ctx, tx, id)
	if err != nil {
		return op, false, err
	}
	if op.Binding != binding {
		return op, false, ErrBinding
	}
	switch op.State {
	case "preparing", "prepared", "approved":
		op.State = "rejected"
		op.CancelReason = "steering"
		if err = updateTx(ctx, tx, op); err != nil {
			return op, false, err
		}
		if err = tx.Commit(ctx); err != nil {
			return op, false, err
		}
		return op, true, nil
	case "rejected":
		return op, true, nil
	default:
		return op, false, nil
	}
}
