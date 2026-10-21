package operations

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
)

// InvalidateConsentTx requires the run row lock. It preserves prepared terms,
// operation identity and expiry, and never rewinds an uncertain dispatch.
func InvalidateConsentTx(ctx context.Context, tx pgx.Tx, runID string) (*domain.Approval, error) {
	var raw []byte
	if err := tx.QueryRow(ctx, "SELECT doc FROM runs WHERE id=$1", runID).Scan(&raw); err != nil {
		return nil, err
	}
	var run domain.Run
	if err := json.Unmarshal(raw, &run); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `UPDATE write_operations SET state='prepared',
 doc=jsonb_set(jsonb_set(doc,'{state}','"prepared"'),'{consent_revision}','0'),updated_at=clock_timestamp()
 WHERE run_id=$1 AND state IN('prepared','approved') RETURNING id::text`, runID)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if run.Approval == nil || run.Approval.OperationID != id {
			continue
		}
		approvalID, idErr := newID()
		if idErr != nil {
			return nil, idErr
		}
		run.Approval.ID = approvalID
		run.Approval.Decision = ""
		run.Approval.DecisionInputID = ""
		run.Approval.ResolutionCallID = ""
		run.Approval.ConsentRevision = 0
		approval, marshalErr := json.Marshal(run.Approval)
		if marshalErr != nil {
			return nil, marshalErr
		}
		if _, err = tx.Exec(
			ctx,
			"UPDATE runs SET doc=jsonb_set(doc,'{approval}',$2::jsonb) WHERE id=$1",
			runID,
			approval,
		); err != nil {
			return nil, err
		}
	}
	return run.Approval, nil
}

// InvalidateConsent checkpoints an observed authorization failure without RPCs.
func (c *Coordinator) InvalidateConsent(parent context.Context, runID string) error {
	return c.invalidateConsent(parent, runID, "")
}

func (c *Coordinator) invalidateConsent(parent context.Context, runID, unsentID string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), time.Second)
	defer cancel()
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, "SELECT id FROM runs WHERE id=$1 FOR UPDATE", runID); err != nil {
		return err
	}
	if unsentID != "" {
		if _, err = tx.Exec(ctx, `UPDATE write_operations SET state='prepared',doc=jsonb_set(doc,'{state}','"prepared"')
 WHERE id=$1 AND run_id=$2 AND state='dispatching'`, unsentID, runID); err != nil {
			return err
		}
	}
	if _, err = InvalidateConsentTx(ctx, tx, runID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (c *Coordinator) invalidateAfterFailure(ctx context.Context, op Operation, failure error) (Operation, error) {
	if err := c.InvalidateConsent(ctx, op.RunID); err != nil {
		return op, err
	}
	fresh, err := c.Get(ctx, op.ID)
	if err != nil {
		return op, err
	}
	return fresh, failure
}

func (c *Coordinator) invalidateUnsentFailure(ctx context.Context, op Operation, failure error) (Operation, error) {
	if err := c.invalidateConsent(ctx, op.RunID, op.ID); err != nil {
		return op, err
	}
	fresh, err := c.Get(ctx, op.ID)
	if err != nil {
		return op, err
	}
	return fresh, failure
}
