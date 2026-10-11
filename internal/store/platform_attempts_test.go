package store_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/store"
)

func TestPlatformAssumesUnknownExposureWithoutChargingUser(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	run, err := s.CreateRun(ctx, p, "run", request())
	require.NoError(t, err)
	run, err = s.ClaimRun(ctx, run.ID, "worker", time.Minute)
	require.NoError(t, err)
	step, err := s.BeginStep(ctx, run.ID, "worker", run.Fence, "model", json.RawMessage(`{}`), 500)
	require.NoError(t, err)
	require.NoError(t, s.AbsorbUnknownStep(ctx, run.ID, "worker", run.Fence, step.ID, 2))
	require.NoError(t, s.AbsorbUnknownStep(ctx, run.ID, "worker", run.Fence, step.ID, 2))
	balance, err := s.Balance(ctx, p)
	require.NoError(t, err)
	require.Zero(t, balance.ReservedMicrocredits)
	require.Zero(t, balance.SpentMicrocredits)
	require.EqualValues(t, 10000, balance.AvailableMicrocredits)
	var exposure int64
	var status string
	require.NoError(
		t,
		s.Pool().
			QueryRow(ctx, "SELECT reserved_exposure,status FROM platform_provider_attempts WHERE step_id=$1", step.ID).
			Scan(&exposure, &status),
	)
	require.EqualValues(t, 500, exposure)
	require.Equal(t, "unknown", status)
	steps, err := s.ExecutionSteps(ctx, run.ID)
	require.NoError(t, err)
	require.JSONEq(t, `{"ErrorCode":"platform_paid_unknown"}`, string(steps[0].Result))
	require.NoError(t, s.Transition(ctx, run.ID, "worker", run.Fence, domain.Completed, "", "done"))
}

func TestSponsoredRestartRepairsOnceAndRequiresExpiredLease(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	run, err := s.CreateRun(ctx, p, "run", request())
	require.NoError(t, err)
	run, err = s.ClaimRun(ctx, run.ID, "worker", time.Minute)
	require.NoError(t, err)
	_, err = s.BeginStep(ctx, run.ID, "worker", run.Fence, "compact", json.RawMessage(`{}`), 400)
	require.NoError(t, err)
	repaired, err := s.RecoverSponsoredRun(ctx, run.ID, 2)
	require.NoError(t, err)
	require.False(t, repaired)
	_, err = s.Pool().Exec(ctx, "UPDATE runs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", run.ID)
	require.NoError(t, err)
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			didRepair, repairErr := store.New(s.Pool(), nil).RecoverSponsoredRun(ctx, run.ID, 2)
			results <- didRepair
			errs <- repairErr
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	for repairErr := range errs {
		require.NoError(t, repairErr)
	}
	repairs := 0
	for result := range results {
		if result {
			repairs++
		}
	}
	require.Equal(t, 1, repairs)
	balance, err := s.Balance(ctx, p)
	require.NoError(t, err)
	require.Zero(t, balance.ReservedMicrocredits)
	require.Zero(t, balance.SpentMicrocredits)
	restored, err := s.ExecutionRun(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, domain.Queued, restored.State)
	_, err = s.ClaimRun(ctx, run.ID, "replacement", time.Minute)
	require.NoError(t, err)
}

func TestPlatformRetryBudgetAndToolsStayProtected(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ctx := context.Background()
	run, err := s.CreateRun(ctx, principal("alice"), "run", request())
	require.NoError(t, err)
	run, err = s.ClaimRun(ctx, run.ID, "worker", time.Minute)
	require.NoError(t, err)
	for range 2 {
		step, stepErr := s.BeginStep(ctx, run.ID, "worker", run.Fence, "model", json.RawMessage(`{}`), 100)
		require.NoError(t, stepErr)
		require.NoError(t, s.AbsorbUnknownStep(ctx, run.ID, "worker", run.Fence, step.ID, 2))
	}
	step, err := s.BeginStep(ctx, run.ID, "worker", run.Fence, "model", json.RawMessage(`{}`), 100)
	require.NoError(t, err)
	code(t, s.AbsorbUnknownStep(ctx, run.ID, "worker", run.Fence, step.ID, 2), "platform_retry_limit")
	count, err := s.SponsoredAttemptCount(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, 2, count)
	balance, err := s.Balance(ctx, principal("alice"))
	require.NoError(t, err)
	require.EqualValues(t, 100, balance.ReservedMicrocredits)
	require.NoError(t, s.CompleteStep(ctx, run.ID, "worker", run.Fence, step.ID, json.RawMessage(`{}`), domain.Usage{}))
	step, err = s.BeginStep(ctx, run.ID, "worker", run.Fence, "tool", json.RawMessage(`{}`), 0)
	require.NoError(t, err)
	code(t, s.AbsorbUnknownStep(ctx, run.ID, "worker", run.Fence, step.ID, 3), "step_not_reconcilable")
	_, err = s.Pool().Exec(ctx, "UPDATE runs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", run.ID)
	require.NoError(t, err)
	repaired, err := s.RecoverSponsoredRun(ctx, run.ID, 3)
	require.NoError(t, err)
	require.False(t, repaired)
}

func TestSponsoredRecoveryNeverRetriesUncertainWrites(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	run, err := s.CreateRun(ctx, p, "run", request())
	require.NoError(t, err)
	run, err = s.ClaimRun(ctx, run.ID, "worker", time.Minute)
	require.NoError(t, err)
	_, err = s.BeginStep(ctx, run.ID, "worker", run.Fence, "model", json.RawMessage(`{}`), 300)
	require.NoError(t, err)
	_, err = s.Pool().
		Exec(ctx, `INSERT INTO write_operations(id,run_id,call_id,state,doc) VALUES(gen_random_uuid(),$1,'call','unknown','{}')`, run.ID)
	require.NoError(t, err)
	_, err = s.Pool().Exec(ctx, "UPDATE runs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", run.ID)
	require.NoError(t, err)
	repaired, err := s.RecoverSponsoredRun(ctx, run.ID, 2)
	require.NoError(t, err)
	require.False(t, repaired)
	balance, err := s.Balance(ctx, p)
	require.NoError(t, err)
	require.EqualValues(t, 300, balance.ReservedMicrocredits)
	count, err := s.SponsoredAttemptCount(ctx, run.ID)
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestClientInterruptionPreventsPlatformAbsorption(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"pause", "cancel"} {
		t.Run(command, func(t *testing.T) {
			t.Parallel()
			s := testStore(t)
			ctx := context.Background()
			p := principal("alice")
			run, err := s.CreateRun(ctx, p, "run", request())
			require.NoError(t, err)
			run, err = s.ClaimRun(ctx, run.ID, "worker", time.Minute)
			require.NoError(t, err)
			step, err := s.BeginStep(ctx, run.ID, "worker", run.Fence, "model", json.RawMessage(`{}`), 500)
			require.NoError(t, err)
			_, err = s.Control(ctx, p, run.ID, "interrupt", command)
			require.NoError(t, err)
			code(t, s.AbsorbUnknownStep(ctx, run.ID, "worker", run.Fence, step.ID, 2), "client_interruption")
			count, err := s.SponsoredAttemptCount(ctx, run.ID)
			require.NoError(t, err)
			require.Zero(t, count)
			balance, err := s.Balance(ctx, p)
			require.NoError(t, err)
			require.EqualValues(t, 500, balance.ReservedMicrocredits)
			require.Zero(t, balance.SpentMicrocredits)
		})
	}
}
