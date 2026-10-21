package operations_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/operations"
)

func TestAuthorityRestorationRequiresFreshConsent(t *testing.T) {
	t.Parallel()
	for _, checkpoint := range []string{"before intent", "after intent"} {
		t.Run(checkpoint, func(t *testing.T) {
			t.Parallel()
			c, req := fixture(t)
			ctx := t.Context()
			b := &backend{}
			op, err := c.Prepare(ctx, req, b)
			require.NoError(t, err)
			op, err = c.Approve(ctx, op.ID, approval(op))
			require.NoError(t, err)
			pending := domain.Run{Approval: &domain.Approval{
				ID: "old-consent", OperationID: op.ID, Decision: "approve", DecisionInputID: "old-input",
				ResolutionCallID: "old-resolution", ConsentRevision: 1, ExpiresAt: op.Preparation.ExpiresAt,
			}}
			raw, err := json.Marshal(pending)
			require.NoError(t, err)
			_, err = c.Pool.Exec(ctx, "UPDATE runs SET doc=$2 WHERE id=$1", req.RunID, raw)
			require.NoError(t, err)
			checks := 0
			revoked := errors.New("revoked: private backend details must remain private")
			authorize := func(context.Context, operations.Operation) error {
				checks++
				if checkpoint == "before intent" || checks == 2 {
					return revoked
				}
				return nil
			}
			op, err = c.Dispatch(ctx, op.ID, operations.Lease{Owner: "worker", Fence: 7}, authorize, b)
			require.ErrorIs(t, err, revoked)
			require.Equal(t, "prepared", op.State)
			require.Zero(t, b.writes.Load())
			var doc []byte
			require.NoError(t, c.Pool.QueryRow(ctx, "SELECT doc FROM runs WHERE id=$1", req.RunID).Scan(&doc))
			var run domain.Run
			require.NoError(t, json.Unmarshal(doc, &run))
			require.NotEqual(t, pending.Approval.ID, run.Approval.ID)
			require.Equal(t, op.ID, run.Approval.OperationID)
			require.Empty(t, run.Approval.Decision)
			require.Empty(t, run.Approval.DecisionInputID)
			require.Empty(t, run.Approval.ResolutionCallID)
			require.Equal(t, pending.Approval.ExpiresAt, run.Approval.ExpiresAt)
			// Reconstruct the coordinator to prove that restoration/restart cannot revive consent.
			c = &operations.Coordinator{Pool: c.Pool}
			_, err = c.Dispatch(ctx, op.ID, operations.Lease{Owner: "worker", Fence: 7}, allow, b)
			require.ErrorIs(t, err, operations.ErrApprovalRequired)
			require.Zero(t, b.writes.Load())
			_, err = c.Approve(ctx, op.ID, approval(op))
			require.NoError(t, err)
			completed, err := c.Dispatch(ctx, op.ID, operations.Lease{Owner: "worker", Fence: 7}, allow, b)
			require.NoError(t, err)
			require.Equal(t, "succeeded", completed.State)
			require.Equal(t, op.ID, completed.ID)
			require.Equal(t, op.Preparation.ExpiresAt, completed.Preparation.ExpiresAt)
			require.Equal(t, []string{op.ID}, b.keys)
		})
	}
}

func TestConsentCheckpointFailureCannotExposePreparedWithOldApproval(t *testing.T) {
	t.Parallel()
	c, req := fixture(t)
	ctx := t.Context()
	b := &backend{}
	op, err := c.Prepare(ctx, req, b)
	require.NoError(t, err)
	_, err = c.Approve(ctx, op.ID, approval(op))
	require.NoError(t, err)
	raw, err := json.Marshal(
		domain.Run{Approval: &domain.Approval{ID: "old-consent", OperationID: op.ID, Decision: "approve"}},
	)
	require.NoError(t, err)
	_, err = c.Pool.Exec(ctx, "UPDATE runs SET doc=$2 WHERE id=$1", req.RunID, raw)
	require.NoError(t, err)
	_, err = c.Pool.Exec(ctx, `CREATE FUNCTION reject_consent_checkpoint() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN RAISE EXCEPTION 'test checkpoint failure'; END $$;
 CREATE TRIGGER reject_consent_checkpoint BEFORE UPDATE OF doc ON runs FOR EACH ROW EXECUTE FUNCTION reject_consent_checkpoint()`)
	require.NoError(t, err)
	checks := 0
	_, err = c.Dispatch(
		ctx,
		op.ID,
		operations.Lease{Owner: "worker", Fence: 7},
		func(context.Context, operations.Operation) error {
			checks++
			if checks == 2 {
				return errors.New("authority revoked")
			}
			return nil
		},
		b,
	)
	require.Error(t, err)
	stored, err := c.Get(ctx, op.ID)
	require.NoError(t, err)
	require.Equal(t, "dispatching", stored.State)
	_, err = c.Pool.Exec(
		ctx,
		"DROP TRIGGER reject_consent_checkpoint ON runs; DROP FUNCTION reject_consent_checkpoint()",
	)
	require.NoError(t, err)
	c = &operations.Coordinator{Pool: c.Pool}
	stored, err = c.Dispatch(ctx, op.ID, operations.Lease{Owner: "worker", Fence: 7}, allow, b)
	require.NoError(t, err)
	require.Equal(t, "dispatching", stored.State)
	require.Zero(t, b.writes.Load())
}
