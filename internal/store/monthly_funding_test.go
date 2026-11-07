package store_test

import (
	"context"
	"encoding/json"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/store"
)

func enableFunding(t *testing.T, s *store.Store, p domain.Principal) {
	t.Helper()
	settings, err := s.GetSettings(t.Context())
	require.NoError(t, err)
	settings.MonthlyFunding = &domain.MonthlyFundingDefaults{
		Enabled:          true,
		BaseMicrocredits: 1000000,
		FactorPPM:        1000000,
	}
	_, err = s.UpdateSettings(t.Context(), p, settings.Version, settings)
	require.NoError(t, err)
}
func previousInitial(t *testing.T, s *store.Store) {
	t.Helper()
	_, err := s.Pool().
		Exec(t.Context(), "UPDATE credit_ledger SET created_at=date_trunc('month',now())-interval '1 month' WHERE kind='initial'")
	require.NoError(t, err)
}
func TestMonthlyFundingTargetsAndZeroReceipts(t *testing.T) {
	t.Parallel()
	for _, balance := range []int64{0, 400000, 1000000, 2000000} {
		t.Run(time.Duration(balance).String(), func(t *testing.T) {
			t.Parallel()
			s := testStore(t)
			p := principal("alice")
			ctx := t.Context()
			uid, err := s.ResolveUser(ctx, p)
			require.NoError(t, err)
			previousInitial(t, s)
			_, err = s.Pool().Exec(ctx, "UPDATE billing_accounts SET balance=$2 WHERE owner_id=$1", uid, balance)
			require.NoError(t, err)
			enableFunding(t, s, p)
			count, err := s.RefillMonthly(ctx, 100)
			require.NoError(t, err)
			require.Equal(t, 1, count)
			status, err := s.MonthlyFunding(ctx, p, uid)
			require.NoError(t, err)
			require.Equal(t, max(int64(0), 1000000-balance), status.LastReceipt.CreditedMicrocredits)
			require.Equal(t, balance, status.LastReceipt.BalanceMicrocredits)
			_, err = s.Pool().Exec(ctx, "UPDATE billing_accounts SET balance=0 WHERE owner_id=$1", uid)
			require.NoError(t, err)
			count, err = store.New(s.Pool(), nil).RefillMonthly(ctx, 100)
			require.NoError(t, err)
			require.Zero(t, count)
			var got int64
			require.NoError(
				t,
				s.Pool().QueryRow(ctx, "SELECT balance FROM billing_accounts WHERE owner_id=$1", uid).Scan(&got),
			)
			require.Zero(t, got)
			require.NotNil(t, status.NextRefillAt)
		})
	}
}
func TestMonthlyFundingPolicyAndFuturePeriod(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ctx := t.Context()
	p := principal("alice")
	uid, err := s.ResolveUser(ctx, p)
	require.NoError(t, err)
	previousInitial(t, s)
	base, factor := int64(2000000), int64(2000000)
	status, err := s.UpdateMonthlyFunding(
		ctx,
		p,
		uid,
		domain.MonthlyFundingPolicy{BaseMicrocredits: &base, FactorPPM: &factor},
	)
	require.NoError(t, err)
	require.EqualValues(t, 1, status.Policy.Version)
	_, err = s.UpdateMonthlyFunding(ctx, p, uid, domain.MonthlyFundingPolicy{})
	require.Error(t, err)
	enableFunding(t, s, p)
	settings, err := s.GetSettings(ctx)
	require.NoError(t, err)
	settings.MonthlyFunding.FactorPPM = 1500000
	_, err = s.UpdateSettings(ctx, p, settings.Version, settings)
	require.NoError(t, err)
	_, err = s.RefillMonthly(ctx, 100)
	require.NoError(t, err)
	status, err = s.MonthlyFunding(ctx, p, uid)
	require.NoError(t, err)
	require.EqualValues(t, 6000000, status.LastReceipt.TargetMicrocredits)
	old := *status.LastReceipt
	factor = 3000000
	status, err = s.UpdateMonthlyFunding(
		ctx,
		p,
		uid,
		domain.MonthlyFundingPolicy{FactorPPM: &factor, BaseMicrocredits: &base, Version: status.Policy.Version},
	)
	require.NoError(t, err)
	require.Equal(t, old, *status.LastReceipt)
	var now time.Time
	require.NoError(t, s.Pool().QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now))
	next := time.Date(now.UTC().Year(), now.UTC().Month()+1, 1, 0, 0, 0, 0, time.UTC)
	_, err = s.RefillMonthlyAtForTest(ctx, next.AddDate(0, 2, 0), 100)
	require.NoError(t, err)
	status, err = s.MonthlyFunding(ctx, p, uid)
	require.NoError(t, err)
	require.EqualValues(t, 9000000, status.LastReceipt.TargetMicrocredits)
	var count int
	require.NoError(
		t,
		s.Pool().QueryRow(ctx, "SELECT count(*) FROM monthly_funding_receipts WHERE owner_id=$1", uid).Scan(&count),
	)
	require.Equal(t, 2, count, "outage does not fund intermediate missed periods")
	huge := int64(math.MaxInt64)
	_, err = s.UpdateMonthlyFunding(
		ctx,
		p,
		uid,
		domain.MonthlyFundingPolicy{BaseMicrocredits: &huge, FactorPPM: &factor, Version: status.Policy.Version},
	)
	require.Error(t, err)
}
func TestMonthlyFundingInitialClassificationAndAuthority(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ctx := t.Context()
	p := principal("existing")
	uid, err := s.ResolveUser(ctx, p)
	require.NoError(t, err)
	enableFunding(t, s, p)
	_, err = s.RefillMonthly(ctx, 100)
	require.NoError(t, err)
	status, err := s.MonthlyFunding(ctx, p, uid)
	require.NoError(t, err)
	require.Equal(t, "initial_credit_period", status.LastReceipt.Reason)
	require.Zero(t, status.LastReceipt.CreditedMicrocredits)
	fresh := principal("new")
	newID, err := s.ResolveUser(ctx, fresh)
	require.NoError(t, err)
	status, err = s.MonthlyFunding(ctx, fresh, newID)
	require.NoError(t, err)
	require.EqualValues(t, 1000000, status.LastReceipt.CreditedMicrocredits)
	var initial int64
	require.NoError(
		t,
		s.Pool().
			QueryRow(ctx, "SELECT amount FROM credit_ledger WHERE owner_id=$1 AND kind='initial'", newID).
			Scan(&initial),
	)
	require.Zero(t, initial)
	service := principal("descriptor")
	service.ServiceAccount = true
	sid, err := s.ResolveUser(ctx, service)
	require.NoError(t, err)
	status, err = s.MonthlyFunding(ctx, p, sid)
	require.NoError(t, err)
	require.False(t, status.Enabled)
	require.Nil(t, status.LastReceipt)
	on := true
	status, err = s.UpdateMonthlyFunding(ctx, p, sid, domain.MonthlyFundingPolicy{Enabled: &on})
	require.NoError(t, err)
	require.True(t, status.Enabled)
	_, err = s.Pool().Exec(ctx, "UPDATE billing_accounts SET monthly_service_account=NULL WHERE owner_id=$1", uid)
	require.NoError(t, err)
	status, err = s.MonthlyFunding(ctx, p, uid)
	require.NoError(t, err)
	require.False(t, status.Enabled)
	plain := principal("plain")
	delete(plain.Permissions, "billing.adjust")
	_, err = s.UpdateMonthlyFunding(ctx, plain, uid, domain.MonthlyFundingPolicy{Enabled: &on})
	require.Error(t, err)
	foreign := p
	foreign.OrgID = "other"
	_, err = s.MonthlyFunding(ctx, foreign, uid)
	require.Error(t, err)
}
func TestMonthlyFundingConcurrentWalletOperations(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ctx := t.Context()
	p := principal("alice")
	r, err := s.CreateRun(ctx, p, "run", request())
	require.NoError(t, err)
	r, err = s.ClaimRun(ctx, r.ID, "worker", time.Minute)
	require.NoError(t, err)
	previousInitial(t, s)
	_, err = s.Pool().Exec(ctx, "UPDATE billing_accounts SET balance=400000 WHERE owner_id=$1", r.OwnerID)
	require.NoError(t, err)
	enableFunding(t, s, p)
	step, err := s.BeginStep(ctx, r.ID, "worker", r.Fence, "model", json.RawMessage(`{}`), 500)
	require.NoError(t, err)
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for range 6 {
		wg.Go(func() {
			_, e := store.New(s.Pool(), nil).RefillMonthly(context.Background(), 100)
			errors <- e
		})
	}
	wg.Go(func() {
		_, e := s.AdjustCredits(ctx, p, r.OwnerID, "bonus", 100, "manual")
		errors <- e
	})
	wg.Go(func() {
		errors <- s.CompleteStep(ctx, r.ID, "worker", r.Fence, step.ID, json.RawMessage(`{}`), domain.Usage{Microcredits: 5})
	})
	wg.Wait()
	close(errors)
	for e := range errors {
		require.NoError(t, e)
	}
	var balance, reserved, credit, used int64
	var receipts int
	require.NoError(
		t,
		s.Pool().
			QueryRow(ctx, "SELECT balance,reserved FROM billing_accounts WHERE owner_id=$1", r.OwnerID).
			Scan(&balance, &reserved),
	)
	require.Zero(t, reserved)
	require.NoError(
		t,
		s.Pool().
			QueryRow(ctx, "SELECT count(*) FROM monthly_funding_receipts WHERE owner_id=$1", r.OwnerID).
			Scan(&receipts),
	)
	require.Equal(t, 1, receipts)
	status, err := s.MonthlyFunding(ctx, p, r.OwnerID)
	require.NoError(t, err)
	credit = status.LastReceipt.CreditedMicrocredits
	used = status.LastReceipt.BalanceMicrocredits
	require.EqualValues(t, 1000000, used+credit)
	require.Equal(t, 400000+credit+100-5, balance)
	var held int64
	require.NoError(
		t,
		s.Pool().QueryRow(ctx, "SELECT reserved FROM billing_accounts WHERE owner_id=$1", r.OwnerID).Scan(&held),
	)
	require.Zero(t, held)
}

func TestMonthlyFundingControlDoesNotLockWalletBeforeRun(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ctx := t.Context()
	p := principal("alice")
	r, err := s.CreateRun(ctx, p, "run", request())
	require.NoError(t, err)
	previousInitial(t, s)
	enableFunding(t, s, p)
	_, err = s.RefillMonthly(ctx, 100)
	require.NoError(t, err)
	conn, err := s.Pool().Acquire(ctx)
	require.NoError(t, err)
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	_, err = tx.Exec(ctx, "SELECT id FROM runs WHERE id=$1 FOR UPDATE", r.ID)
	require.NoError(t, err)
	blocked := make(chan error, 1)
	go func() { _, e := s.Control(ctx, p, r.ID, "pause", "pause"); blocked <- e }()
	require.Eventually(t, func() bool {
		var waiting bool
		e := s.Pool().
			QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))", conn.Conn().PgConn().PID()).
			Scan(&waiting)
		return e == nil && waiting
	}, time.Second*5, time.Millisecond*10)
	_, err = s.AdjustCredits(ctx, p, r.OwnerID, "parallel", 1, "concurrent adjustment")
	require.NoError(t, err)
	require.NoError(t, tx.Rollback(ctx))
	require.NoError(t, <-blocked)
}

func TestMonthlyFundingGlobalOverflowPreservesSettings(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ctx := t.Context()
	p := principal("alice")
	uid, err := s.ResolveUser(ctx, p)
	require.NoError(t, err)
	settings, err := s.GetSettings(ctx)
	require.NoError(t, err)
	settings.MonthlyFunding = &domain.MonthlyFundingDefaults{Enabled: true, BaseMicrocredits: 1, FactorPPM: 0}
	settings, err = s.UpdateSettings(ctx, p, settings.Version, settings)
	require.NoError(t, err)
	huge := int64(math.MaxInt64)
	_, err = s.UpdateMonthlyFunding(ctx, p, uid, domain.MonthlyFundingPolicy{BaseMicrocredits: &huge})
	require.NoError(t, err)
	settings.MonthlyFunding.FactorPPM = 2000000
	_, err = s.UpdateSettings(ctx, p, settings.Version, settings)
	require.ErrorContains(t, err, "monthly_funding_overflow")
	unchanged, err := s.GetSettings(ctx)
	require.NoError(t, err)
	require.Zero(t, unchanged.MonthlyFunding.FactorPPM)
}

func TestMonthlyFundingLocksOwnerBeforeWalletReceipt(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ctx := t.Context()
	p := principal("alice")
	uid, err := s.ResolveUser(ctx, p)
	require.NoError(t, err)
	previousInitial(t, s)
	enableFunding(t, s, p)
	conn, err := s.Pool().Acquire(ctx)
	require.NoError(t, err)
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	_, err = tx.Exec(ctx, "SELECT id FROM users WHERE id=$1 FOR UPDATE", uid)
	require.NoError(t, err)
	result := make(chan error, 1)
	go func() { _, e := s.RefillMonthly(ctx, 100); result <- e }()
	require.Eventually(t, func() bool {
		var waiting bool
		e := s.Pool().
			QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))", conn.Conn().PgConn().PID()).
			Scan(&waiting)
		return e == nil && waiting
	}, time.Second*5, time.Millisecond*10)
	_, err = tx.Exec(ctx, "UPDATE billing_accounts SET balance=balance+1 WHERE owner_id=$1", uid)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
	require.NoError(t, <-result)
}
