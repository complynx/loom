package operations_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/complynx/clx-scripter/internal/operations"
	"github.com/complynx/clx-scripter/internal/store"
)

type backend struct {
	writes         atomic.Int32
	failAfterWrite bool
	cancel         context.CancelFunc
	expiry         time.Time
	keys           []string
}

func (b *backend) Prepare(context.Context, operations.Operation) (operations.Preparation, error) {
	return operations.Preparation{
		Token:         "confirmation",
		Preconditions: json.RawMessage(`{"version":7}`),
		ExpiresAt:     b.expiry,
	}, nil
}
func (b *backend) Execute(_ context.Context, op operations.Operation) (operations.Result, error) {
	b.writes.Add(1)
	b.keys = append(b.keys, op.ID)
	if b.cancel != nil {
		b.cancel()
	}
	if b.failAfterWrite {
		return operations.Result{}, context.DeadlineExceeded
	}
	return operations.Result{State: "succeeded", Output: json.RawMessage(`{"reservation":"saved"}`)}, nil
}
func (b *backend) Status(context.Context, operations.Operation) (operations.Result, error) {
	return operations.Result{State: "succeeded", Output: json.RawMessage(`{"reservation":"saved"}`)}, nil
}

func fixture(t *testing.T) (*operations.Coordinator, operations.Request) {
	t.Helper()
	dsn := os.Getenv("CLX_TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("CLX_TEST_DATABASE_DSN required")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	schema := "operations_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		if cleanupErr != nil {
			t.Errorf("cleanup: %v", cleanupErr)
		}
		admin.Close()
	})
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	q := parsed.Query()
	q.Set("search_path", schema)
	parsed.RawQuery = q.Encode()
	require.NoError(t, store.Migrate(ctx, parsed.String()))
	pool, err := pgxpool.New(ctx, parsed.String())
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	user, chat, run := uuid.NewString(), uuid.NewString(), uuid.NewString()
	_, err = pool.Exec(ctx, `INSERT INTO users(id,issuer,subject) VALUES($1,'test','alice')`, user)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO chats(id,owner_id,org_id) VALUES($1,$2,'org')`, chat, user)
	require.NoError(t, err)
	_, err = pool.Exec(
		ctx,
		`INSERT INTO runs(id,chat_id,owner_id,actor_id,org_id,state,doc,lease_owner,lease_until,fence,max_microcredits) VALUES($1,$2,$3,'frontend','org','running','{}','worker',clock_timestamp()+interval '1 hour',7,1000)`,
		run,
		chat,
		user,
	)
	require.NoError(t, err)
	return &operations.Coordinator{
		Pool: pool,
	}, operations.Request{
		RunID:     run,
		CallID:    "step1/call1/1",
		Module:    "orders",
		Method:    "reserve",
		Mode:      "service_id",
		Arguments: json.RawMessage(`{"order":42}`),
		Binding: operations.Binding{
			UserID:       user,
			Issuer:       "test",
			Subject:      "alice",
			Actor:        "frontend",
			OrgID:        "org",
			ContractHash: "v1",
		},
	}
}
func allow(context.Context, operations.Operation) error { return nil }
func approval(op operations.Operation) operations.Approval {
	return operations.Approval{Binding: op.Binding, BindingHash: op.BindingHash, Approve: true}
}

func TestWriteRequiresExactApprovalAndIsDispatchedOnce(t *testing.T) {
	t.Parallel()
	c, req := fixture(t)
	ctx := context.Background()
	b := &backend{}
	op, err := c.Prepare(ctx, req, b)
	require.NoError(t, err)
	_, err = c.Dispatch(ctx, op.ID, operations.Lease{Owner: "worker", Fence: 7}, allow, b)
	require.ErrorIs(t, err, operations.ErrApprovalRequired)
	require.Zero(t, b.writes.Load())
	changed := req
	changed.Arguments = json.RawMessage(`{"order":43}`)
	_, err = c.Prepare(ctx, changed, b)
	require.ErrorIs(t, err, operations.ErrBinding)
	bad := approval(op)
	bad.Binding.ContractHash = "v2"
	_, err = c.Approve(ctx, op.ID, bad)
	require.ErrorIs(t, err, operations.ErrBinding)
	bad = approval(op)
	bad.BindingHash = "changed-preconditions"
	_, err = c.Approve(ctx, op.ID, bad)
	require.ErrorIs(t, err, operations.ErrBinding)
	_, err = c.Approve(ctx, op.ID, approval(op))
	require.NoError(t, err)
	result, err := c.Dispatch(ctx, op.ID, operations.Lease{Owner: "worker", Fence: 7}, allow, b)
	require.NoError(t, err)
	require.Equal(t, "succeeded", result.State)
	again, err := c.Prepare(ctx, req, b)
	require.NoError(t, err)
	require.Equal(t, op.ID, again.ID)
	_, err = c.Dispatch(ctx, op.ID, operations.Lease{Owner: "worker", Fence: 7}, allow, b)
	require.NoError(t, err)
	require.EqualValues(t, 1, b.writes.Load())
	require.Equal(t, []string{op.ID}, b.keys)
}
func TestLostReplyReconcilesWithoutRepeatingWrite(t *testing.T) {
	t.Parallel()
	c, req := fixture(t)
	ctx := context.Background()
	b := &backend{failAfterWrite: true}
	op, err := c.Prepare(ctx, req, b)
	require.NoError(t, err)
	_, err = c.Approve(ctx, op.ID, approval(op))
	require.NoError(t, err)
	op, err = c.Dispatch(ctx, op.ID, operations.Lease{Owner: "worker", Fence: 7}, allow, b)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, "unknown", op.State)
	_, err = c.Dispatch(ctx, op.ID, operations.Lease{Owner: "worker", Fence: 7}, allow, b)
	require.NoError(t, err)
	require.EqualValues(t, 1, b.writes.Load())
	op, err = c.Reconcile(ctx, op.ID, b)
	require.NoError(t, err)
	require.Equal(t, "succeeded", op.State)
	require.EqualValues(t, 1, b.writes.Load())
}
func TestAuthorityLeaseAndExpiryBlockEffects(t *testing.T) {
	t.Parallel()
	c, req := fixture(t)
	ctx := context.Background()
	b := &backend{}
	op, err := c.Prepare(ctx, req, b)
	require.NoError(t, err)
	_, err = c.Approve(ctx, op.ID, approval(op))
	require.NoError(t, err)
	revoked := errors.New("permission revoked")
	_, err = c.Dispatch(
		ctx,
		op.ID,
		operations.Lease{Owner: "worker", Fence: 7},
		func(context.Context, operations.Operation) error { return revoked },
		b,
	)
	require.ErrorIs(t, err, revoked)
	_, err = c.Dispatch(ctx, op.ID, operations.Lease{Owner: "worker", Fence: 6}, allow, b)
	require.ErrorIs(t, err, operations.ErrLease)
	_, err = c.Pool.Exec(ctx, `UPDATE runs SET cancel_requested=true WHERE id=$1`, req.RunID)
	require.NoError(t, err)
	_, err = c.Dispatch(ctx, op.ID, operations.Lease{Owner: "worker", Fence: 7}, allow, b)
	require.ErrorIs(t, err, operations.ErrLease)
	require.Zero(t, b.writes.Load())
	req.CallID = "expired"
	b.expiry = time.Now().Add(-time.Second)
	_, err = c.Prepare(ctx, req, b)
	require.ErrorIs(t, err, operations.ErrExpired)
	require.Zero(t, b.writes.Load())
}

func TestConcurrentDispatchSharesOneStableIntent(t *testing.T) {
	t.Parallel()
	c, req := fixture(t)
	ctx := context.Background()
	b := &backend{}
	op, err := c.Prepare(ctx, req, b)
	require.NoError(t, err)
	_, err = c.Approve(ctx, op.ID, approval(op))
	require.NoError(t, err)
	results := make(chan error, 12)
	for range 12 {
		go func() {
			_, dispatchErr := c.Dispatch(ctx, op.ID, operations.Lease{Owner: "worker", Fence: 7}, allow, b)
			results <- dispatchErr
		}()
	}
	for range 12 {
		require.NoError(t, <-results)
	}
	require.EqualValues(t, 1, b.writes.Load())
	require.Equal(t, []string{op.ID}, b.keys)
}

func TestExactParameterApprovalCannotOutlivePreparation(t *testing.T) {
	t.Parallel()
	c, req := fixture(t)
	ctx := context.Background()
	req.Mode = "exact_parameters"
	b := &backend{expiry: time.Now().Add(100 * time.Millisecond)}
	op, err := c.Prepare(ctx, req, b)
	require.NoError(t, err)
	time.Sleep(time.Until(op.Preparation.ExpiresAt) + time.Millisecond)
	_, err = c.Approve(ctx, op.ID, approval(op))
	require.ErrorIs(t, err, operations.ErrExpired)
	require.Zero(t, b.writes.Load())
}

func TestCancellationDuringFreshAuthorizationPreventsDispatch(t *testing.T) {
	t.Parallel()
	c, req := fixture(t)
	ctx := context.Background()
	b := &backend{}
	op, err := c.Prepare(ctx, req, b)
	require.NoError(t, err)
	_, err = c.Approve(ctx, op.ID, approval(op))
	require.NoError(t, err)
	var checks int
	authorize := func(ctx context.Context, _ operations.Operation) error {
		checks++
		if checks == 2 {
			_, cancelErr := c.Pool.Exec(ctx, `UPDATE runs SET cancel_requested=true WHERE id=$1`, req.RunID)
			return cancelErr
		}
		return nil
	}
	op, err = c.Dispatch(ctx, op.ID, operations.Lease{Owner: "worker", Fence: 7}, authorize, b)
	require.ErrorIs(t, err, operations.ErrLease)
	require.Equal(t, "failed", op.State)
	require.Zero(t, b.writes.Load())
}

func TestRepeatedRejectionNeverDispatches(t *testing.T) {
	t.Parallel()
	c, req := fixture(t)
	ctx := context.Background()
	b := &backend{}
	op, err := c.Prepare(ctx, req, b)
	require.NoError(t, err)
	decision := approval(op)
	decision.Approve = false
	_, err = c.Approve(ctx, op.ID, decision)
	require.NoError(t, err)
	_, err = c.Approve(ctx, op.ID, decision)
	require.NoError(t, err)
	op, err = c.Dispatch(ctx, op.ID, operations.Lease{Owner: "worker", Fence: 7}, allow, b)
	require.NoError(t, err)
	require.Equal(t, "rejected", op.State)
	require.Zero(t, b.writes.Load())
}

func TestClientCancellationStillPersistsUnknownWriteOutcome(t *testing.T) {
	t.Parallel()
	c, req := fixture(t)
	base := context.Background()
	ctx, cancel := context.WithCancel(base)
	defer cancel()
	b := &backend{failAfterWrite: true, cancel: cancel}
	op, err := c.Prepare(ctx, req, b)
	require.NoError(t, err)
	_, err = c.Approve(ctx, op.ID, approval(op))
	require.NoError(t, err)
	op, err = c.Dispatch(ctx, op.ID, operations.Lease{Owner: "worker", Fence: 7}, allow, b)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	saved, err := c.Get(base, op.ID)
	require.NoError(t, err)
	require.Equal(t, "unknown", saved.State)
	saved, err = c.Reconcile(base, op.ID, b)
	require.NoError(t, err)
	require.Equal(t, "succeeded", saved.State)
	require.EqualValues(t, 1, b.writes.Load())
}

func TestSteeringDiscardsUndispatchedIntentWithoutEffects(t *testing.T) {
	t.Parallel()
	c, req := fixture(t)
	ctx := context.Background()
	b := &backend{}
	_, exists, err := c.FindByCall(ctx, req.RunID, req.CallID)
	require.NoError(t, err)
	require.False(t, exists)
	op, err := c.Prepare(ctx, req, b)
	require.NoError(t, err)
	_, err = c.Approve(ctx, op.ID, approval(op))
	require.NoError(t, err)
	found, exists, err := c.FindByCall(ctx, req.RunID, req.CallID)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, op.ID, found.ID)
	op, discarded, err := c.CancelUndispatched(ctx, op.ID, operations.Lease{Owner: "worker", Fence: 7}, op.Binding)
	require.NoError(t, err)
	require.True(t, discarded)
	require.Equal(t, "steering", op.CancelReason)
	_, discarded, err = c.CancelUndispatched(ctx, op.ID, operations.Lease{Owner: "worker", Fence: 7}, op.Binding)
	require.NoError(t, err)
	require.True(t, discarded)
	_, err = c.Dispatch(ctx, op.ID, operations.Lease{Owner: "worker", Fence: 7}, allow, b)
	require.NoError(t, err)
	require.Zero(t, b.writes.Load())
}

func TestSteeringCannotHideUnknownRemoteOutcome(t *testing.T) {
	t.Parallel()
	c, req := fixture(t)
	ctx := context.Background()
	b := &backend{failAfterWrite: true}
	op, err := c.Prepare(ctx, req, b)
	require.NoError(t, err)
	_, err = c.Approve(ctx, op.ID, approval(op))
	require.NoError(t, err)
	op, err = c.Dispatch(ctx, op.ID, operations.Lease{Owner: "worker", Fence: 7}, allow, b)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	op, discarded, err := c.CancelUndispatched(ctx, op.ID, operations.Lease{Owner: "worker", Fence: 7}, op.Binding)
	require.NoError(t, err)
	require.False(t, discarded)
	require.Equal(t, "unknown", op.State)
	op, err = c.Reconcile(ctx, op.ID, b)
	require.NoError(t, err)
	require.Equal(t, "succeeded", op.State)
	require.EqualValues(t, 1, b.writes.Load())
}

func TestStaleOrForeignSteeringCannotDiscardWrite(t *testing.T) {
	t.Parallel()
	c, req := fixture(t)
	ctx := context.Background()
	b := &backend{}
	op, err := c.Prepare(ctx, req, b)
	require.NoError(t, err)
	_, _, err = c.CancelUndispatched(ctx, op.ID, operations.Lease{Owner: "worker", Fence: 6}, op.Binding)
	require.ErrorIs(t, err, operations.ErrLease)
	foreign := op.Binding
	foreign.Subject = "bob"
	_, _, err = c.CancelUndispatched(ctx, op.ID, operations.Lease{Owner: "worker", Fence: 7}, foreign)
	require.ErrorIs(t, err, operations.ErrBinding)
	saved, err := c.Get(ctx, op.ID)
	require.NoError(t, err)
	require.Equal(t, "prepared", saved.State)
	require.Zero(t, b.writes.Load())
}
