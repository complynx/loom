package operations

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	ExternalConfirmOnce     = "once"
	ExternalConfirmMethod   = "method"
	ExternalConfirmAll      = "all"
	ExternalReject          = "reject"
	externalMaxPending      = 8
	externalPrepareTimeout  = 4 * time.Minute
	externalPrepareRecovery = externalPrepareTimeout + time.Minute
	externalDispatchTimeout = 5 * time.Minute
	externalRecoveryDelay   = externalDispatchTimeout + time.Minute
)

var ErrExternalPendingLimit = errors.New("too many pending external approvals")

// Lock the profile before the session. Revoking a profile takes the same locks.
func lockExternalAuthority(ctx context.Context, tx pgx.Tx, op Operation) error {
	var owner string
	err := tx.QueryRow(ctx, `SELECT p.owner_id FROM external_agent_profiles p JOIN users u ON u.id=p.owner_id
		WHERE u.id=$1 AND u.issuer=$2 AND u.subject=$3 AND p.org_id=$4
			AND p.enabled AND p.writes AND p.confirmed_by=$5 FOR UPDATE OF p`,
		op.Binding.UserID, op.Binding.Issuer, op.Binding.Subject, op.Binding.OrgID, op.ExternalConfirmer).Scan(&owner)
	if err != nil {
		return err
	}
	var id string
	return tx.QueryRow(ctx, `SELECT id FROM external_agent_sessions
		WHERE id=$1 AND owner_id=$2 AND org_id=$3 AND client_id=$4 AND expires_at>clock_timestamp() FOR UPDATE`,
		op.ExternalSessionID, owner, op.Binding.OrgID, op.ExternalClient).Scan(&id)
}

func (c *Coordinator) insertExternalPreparation(
	ctx context.Context,
	op Operation,
	raw []byte,
) (pgconn.CommandTag, error) {
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	defer rollbackExternal(ctx, tx)
	if err = lockExternalAuthority(ctx, tx, op); err != nil {
		return pgconn.CommandTag{}, err
	}
	pending, err := externalPendingForNewCall(ctx, tx, op)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	if pending < 0 {
		return pgconn.CommandTag{}, nil
	}
	if pending >= externalMaxPending {
		return pgconn.CommandTag{}, ErrExternalPendingLimit
	}
	tag, err := tx.Exec(ctx, `INSERT INTO write_operations(id,external_session_id,call_id,state,doc)
		VALUES ($1,$2,$3,'preparing',$4) ON CONFLICT(external_session_id,call_id) DO NOTHING`,
		op.ID, op.ExternalSessionID, op.CallID, raw)
	if err != nil {
		return tag, err
	}
	return tag, tx.Commit(ctx)
}

// A retry can always read its original operation, even when the quota is full.
func externalPendingForNewCall(ctx context.Context, tx pgx.Tx, op Operation) (int, error) {
	var pending int
	err := tx.QueryRow(ctx, `SELECT CASE WHEN EXISTS (
			SELECT 1 FROM write_operations WHERE external_session_id=$3 AND call_id=$4
		) THEN -1 ELSE (
			SELECT count(*) FROM write_operations w JOIN external_agent_sessions x ON x.id=w.external_session_id
			WHERE x.owner_id=$1 AND x.org_id=$2 AND x.expires_at>clock_timestamp()
			AND (w.state='preparing' AND w.updated_at>clock_timestamp()-make_interval(secs=>$5)
			OR w.state='prepared' AND (w.doc->'preparation'->>'expires_at')::timestamptz>clock_timestamp())
		) END`, op.Binding.UserID, op.Binding.OrgID, op.ExternalSessionID, op.CallID,
		externalPrepareRecovery.Seconds()).Scan(&pending)
	return pending, err
}

// FindExternalPreparation resolves a retry using the original caller binding
// and arguments. The sealed execution contract belongs to the saved operation.
func (c *Coordinator) FindExternalPreparation(ctx context.Context, req Request) (Operation, error) {
	args, err := canonical(req.Arguments)
	if err != nil {
		return Operation{}, ErrBinding
	}
	var raw []byte
	err = c.Pool.QueryRow(ctx, `SELECT doc FROM write_operations WHERE external_session_id=$1 AND call_id=$2`,
		req.ExternalSessionID, req.CallID).Scan(&raw)
	if err != nil {
		return Operation{}, err
	}
	var op Operation
	if err = json.Unmarshal(raw, &op); err != nil {
		return Operation{}, err
	}
	binding := op.Binding
	binding.ContractHash = req.Binding.ContractHash
	if binding != req.Binding || op.ExternalSessionID != req.ExternalSessionID ||
		op.ExternalService != req.ExternalService || op.ExternalClient != req.ExternalClient ||
		op.ExternalConfirmer != req.ExternalConfirmer || op.Module != req.Module || op.Method != req.Method ||
		!c.matchesExternalArguments(op, args) {
		return Operation{}, ErrBinding
	}
	if op.State == statePreparing {
		return c.recoverExternalPreparation(ctx, op)
	}
	return op, nil
}

// A crashed preparation can be closed without repeating the backend request.
// A live preparation can still publish its receipt under the same operation ID.
func (c *Coordinator) recoverExternalPreparation(ctx context.Context, op Operation) (Operation, error) {
	_, err := c.Pool.Exec(ctx, `UPDATE write_operations SET state='failed', updated_at=clock_timestamp(),
		doc=doc||'{"state":"failed","failure_code":"preparation_interrupted"}'::jsonb
		WHERE id=$1 AND external_session_id=$2 AND state='preparing'
		AND updated_at<=clock_timestamp()-make_interval(secs=>$3)`,
		op.ID, op.ExternalSessionID, externalPrepareRecovery.Seconds())
	if err != nil {
		return op, err
	}
	return c.Get(ctx, op.ID)
}

func rollbackExternal(parent context.Context, tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func externalTarget(op Operation) string {
	return op.ExternalService + "/" + op.Module + "/" + op.Method
}

// DecideExternal checks the delegated chat owner and consumes the pending
// approval. A remembered rule is committed with that exact decision.
func (c *Coordinator) DecideExternal(
	ctx context.Context, id, issuer, subject, org, confirmer, decision string,
) (Operation, error) {
	op, err := c.Get(ctx, id)
	if err != nil {
		return op, err
	}
	if op.ExternalSessionID == "" || op.Binding.Issuer != issuer || op.Binding.Subject != subject ||
		op.Binding.OrgID != org || op.ExternalConfirmer != confirmer || op.Mode == noConfirmation {
		return op, ErrBinding
	}
	target := ""
	switch decision {
	case ExternalConfirmOnce, ExternalReject:
	case ExternalConfirmMethod:
		target = externalTarget(op)
	case ExternalConfirmAll:
		target = "*"
	default:
		return op, ErrBinding
	}
	if target != "" && !op.RememberApproval {
		return op, ErrBinding
	}
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return op, err
	}
	defer rollbackExternal(ctx, tx)
	if err = lockExternalAuthority(ctx, tx, op); err != nil {
		return op, err
	}
	op, err = locked(ctx, tx, id)
	if err != nil {
		return op, err
	}
	if op.ExternalDecision != "" {
		if op.ExternalDecision != decision {
			return op, ErrState
		}
		return op, nil
	}
	if op.State != statePrepared {
		return op, ErrState
	}
	op.ExternalDecision = decision
	op, err = approveLocked(ctx, tx, op, Approval{
		Binding: op.Binding, BindingHash: op.BindingHash, Approve: decision != ExternalReject,
	})
	if err != nil {
		return op, err
	}
	if target != "" {
		_, err = tx.Exec(ctx, `INSERT INTO external_agent_approval_rules(session_id,target) VALUES($1,$2)
			ON CONFLICT DO NOTHING`, op.ExternalSessionID, target)
		if err != nil {
			return op, err
		}
	}
	return op, tx.Commit(ctx)
}

func (c *Coordinator) DispatchExternal(
	ctx context.Context, op Operation, authorize Authorize, backend Backend,
) (Operation, error) {
	if op.ExternalSessionID == "" || authorize == nil {
		return op, ErrBinding
	}
	args, err := c.externalArguments(op)
	if err != nil {
		return op, err
	}
	if err = authorize(ctx, op.withArguments(args)); err != nil {
		return op, err
	}
	op, err = c.claimExternalDispatch(ctx, op)
	if err != nil {
		return op, err
	}
	return c.executeExternalIntent(ctx, op, authorize, backend)
}

func (c *Coordinator) claimExternalDispatch(ctx context.Context, op Operation) (Operation, error) {
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return op, err
	}
	defer rollbackExternal(ctx, tx)
	if err = lockExternalAuthority(ctx, tx, op); err != nil {
		return op, err
	}
	op, err = locked(ctx, tx, op.ID)
	if err != nil {
		return op, err
	}
	if op.State != statePrepared && op.State != stateApproved {
		return op, ErrState
	}
	if !op.Preparation.ExpiresAt.After(time.Now()) {
		return op, ErrExpired
	}
	if op.State == statePrepared {
		var remembered bool
		if op.RememberApproval {
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM external_agent_approval_rules
				WHERE session_id=$1 AND target IN('*',$2)
				AND created_at<=(SELECT created_at FROM write_operations WHERE id=$3))`,
				op.ExternalSessionID, externalTarget(op), op.ID).Scan(&remembered)
			if err != nil {
				return op, err
			}
		}
		if !remembered {
			return op, ErrApprovalRequired
		}
	}
	hash, err := seal(op)
	if err != nil || op.BindingHash != hash {
		return op, ErrBinding
	}
	op.State = stateDispatching
	if err = updateTx(ctx, tx, op); err != nil {
		return op, err
	}
	return op, tx.Commit(ctx)
}

func (c *Coordinator) executeExternalIntent(
	ctx context.Context, op Operation, authorize Authorize, backend Backend,
) (Operation, error) {
	ctx, cancel := context.WithTimeout(ctx, externalDispatchTimeout)
	defer cancel()
	args, err := c.externalArguments(op)
	if err != nil {
		return c.invalidateUnsentFailure(ctx, op, err)
	}
	request := op.withArguments(args)
	if err = authorize(ctx, request); err != nil {
		return c.invalidateUnsentFailure(ctx, op, err)
	}
	result, err := backend.Execute(ctx, request)
	return c.saveExecutionResult(ctx, op, result, err)
}

func (c *Coordinator) ReconcileExternal(ctx context.Context, op Operation, backend Backend) (Operation, error) {
	if op.ExternalSessionID == "" {
		return op, ErrBinding
	}
	if op.State == statePreparing {
		return c.recoverExternalPreparation(ctx, op)
	}
	if op.State == stateDispatching {
		// An in-flight execute still owns its receipt. After the request
		// deadline, only status lookup can reconcile an interrupted dispatch.
		var ready bool
		err := c.Pool.QueryRow(ctx, `SELECT updated_at<clock_timestamp()-make_interval(secs=>$2)
			FROM write_operations WHERE id=$1`, op.ID, externalRecoveryDelay.Seconds()).Scan(&ready)
		if err != nil || !ready {
			return op, err
		}
	}
	return c.Reconcile(ctx, op.ID, backend)
}
