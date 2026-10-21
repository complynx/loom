package operations_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/operations"
)

type adoptionBackend struct {
	inspectionError error
	inspection      operations.Inspection
	prepares        int
	inspections     int
	executed        []string
	statuses        []string
}

func (b *adoptionBackend) Prepare(context.Context, operations.Operation) (operations.Preparation, error) {
	b.prepares++
	return operations.Preparation{}, operations.ErrState
}
func (b *adoptionBackend) Inspect(context.Context, operations.Operation) (operations.Inspection, error) {
	b.inspections++
	return b.inspection, b.inspectionError
}
func (b *adoptionBackend) Execute(_ context.Context, op operations.Operation) (operations.Result, error) {
	b.executed = append(b.executed, op.BackendID())
	return operations.Result{State: "succeeded", Output: json.RawMessage(`{"receipt":"original"}`)}, nil
}
func (b *adoptionBackend) Status(_ context.Context, op operations.Operation) (operations.Result, error) {
	b.statuses = append(b.statuses, op.BackendID())
	return operations.Result{State: "succeeded", Output: json.RawMessage(`{"receipt":"original"}`)}, nil
}

func TestAdoptionPreservesNativeIdentityTermsAndConsent(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"prepared", "succeeded", "unknown"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			c, req := fixture(t)
			req.BackendOperationID = "native-operation"
			expiry := time.Now().UTC().Add(30 * time.Minute).Truncate(time.Microsecond)
			b := &adoptionBackend{inspection: operations.Inspection{
				Preparation: operations.Preparation{
					Arguments:   json.RawMessage(`{"order":42}`),
					Description: "Original native terms",
					Token:       "native-confirmation",
					ExpiresAt:   expiry,
				},
				Result: operations.Result{State: state, Output: json.RawMessage(`{"receipt":"original"}`)},
			}}
			op, err := c.Prepare(t.Context(), req, b)
			require.NoError(t, err)
			require.NotEqual(t, req.BackendOperationID, op.ID)
			require.Equal(t, req.BackendOperationID, op.BackendID())
			require.Equal(t, expiry, op.Preparation.ExpiresAt)
			require.Equal(t, "Original native terms", op.Preparation.Description)
			replayed, err := c.Prepare(t.Context(), req, b)
			require.NoError(t, err)
			require.Equal(t, op.ID, replayed.ID)
			require.Equal(t, 1, b.inspections)
			require.Zero(t, b.prepares)
			switch state {
			case "prepared":
				_, err = c.Dispatch(t.Context(), op.ID, operations.Lease{Owner: "worker", Fence: 7}, allow, b)
				require.ErrorIs(t, err, operations.ErrApprovalRequired)
				_, err = c.Approve(t.Context(), op.ID, approval(op))
				require.NoError(t, err)
				_, err = c.Dispatch(t.Context(), op.ID, operations.Lease{Owner: "worker", Fence: 7}, allow, b)
				require.NoError(t, err)
				require.Equal(t, []string{req.BackendOperationID}, b.executed)
			case "unknown":
				_, err = c.Reconcile(t.Context(), op.ID, b)
				require.NoError(t, err)
				require.Equal(t, []string{req.BackendOperationID}, b.statuses)
				require.Empty(t, b.executed)
			case "succeeded":
				require.JSONEq(t, `{"receipt":"original"}`, string(op.Result))
				require.Empty(t, b.executed)
			}
			req.BackendOperationID = "replacement-is-not-the-same-intent"
			_, err = c.Prepare(t.Context(), req, b)
			require.ErrorIs(t, err, operations.ErrBinding)
			require.Equal(t, 1, b.inspections)
		})
	}
}

func TestAdoptionNeverRenewsExpiredPreparationOrFallsBack(t *testing.T) {
	t.Parallel()
	c, req := fixture(t)
	req.BackendOperationID = "expired-native"
	b := &adoptionBackend{inspection: operations.Inspection{
		Preparation: operations.Preparation{
			Arguments: req.Arguments,
			Token:     "native",
			ExpiresAt: time.Now().Add(-time.Second),
		},
		Result: operations.Result{State: "prepared"},
	}}
	_, err := c.Prepare(t.Context(), req, b)
	require.ErrorIs(t, err, operations.ErrExpired)
	require.Zero(t, b.prepares)
	require.Empty(t, b.executed)
	req.CallID = "unsupported"
	_, err = c.Prepare(t.Context(), req, &backend{})
	require.ErrorIs(t, err, operations.ErrAdoptionUnsupported)
}

func TestAdoptionInspectionFailureRetainsOriginalForStatusOnly(t *testing.T) {
	t.Parallel()
	c, req := fixture(t)
	req.BackendOperationID = "native-unknown"
	b := &adoptionBackend{inspectionError: context.DeadlineExceeded}
	op, err := c.Prepare(t.Context(), req, b)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, "unknown", op.State)
	stored, err := c.Get(t.Context(), op.ID)
	require.NoError(t, err)
	require.Equal(t, "native-unknown", stored.BackendID())
	_, err = c.Dispatch(t.Context(), op.ID, operations.Lease{Owner: "worker", Fence: 7}, allow, b)
	require.NoError(t, err)
	require.Empty(t, b.executed)
	_, err = c.Reconcile(t.Context(), op.ID, b)
	require.NoError(t, err)
	require.Equal(t, []string{"native-unknown"}, b.statuses)
	require.Zero(t, b.prepares)
}

func TestAdoptionSealRejectsChangedBackendIdentity(t *testing.T) {
	t.Parallel()
	c, req := fixture(t)
	req.BackendOperationID = "native-original"
	b := &adoptionBackend{inspection: operations.Inspection{
		Preparation: operations.Preparation{
			Arguments: req.Arguments,
			Token:     "native",
			ExpiresAt: time.Now().Add(time.Minute),
		},
		Result: operations.Result{State: "prepared"},
	}}
	op, err := c.Prepare(t.Context(), req, b)
	require.NoError(t, err)
	_, err = c.Approve(t.Context(), op.ID, approval(op))
	require.NoError(t, err)
	_, err = c.Pool.Exec(
		t.Context(),
		`UPDATE write_operations SET doc=jsonb_set(doc,'{backend_operation_id}','"foreign"') WHERE id=$1`,
		op.ID,
	)
	require.NoError(t, err)
	_, err = c.Dispatch(t.Context(), op.ID, operations.Lease{Owner: "worker", Fence: 7}, allow, b)
	require.ErrorIs(t, err, operations.ErrBinding)
	require.Empty(t, b.executed)
}
