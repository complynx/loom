package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
)

func TestProviderRetryFencesAndSettlesKnownRejectionAtomically(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	run, err := s.CreateRun(ctx, p, "retry-run", request())
	require.NoError(t, err)
	run, err = s.ClaimRun(ctx, run.ID, "worker", time.Minute)
	require.NoError(t, err)
	step, err := s.BeginStep(ctx, run.ID, "worker", run.Fence, "model", json.RawMessage(`{}`), 500)
	require.NoError(t, err)
	retry := domain.ProviderRetry{
		Attempts:      1,
		NextAttemptAt: time.Now().Add(time.Second),
		Deadline:      time.Now().Add(time.Minute),
		Code:          "rate_limit_exceeded",
	}
	_, err = s.DeferProviderRetry(ctx, run.ID, "other-worker", run.Fence, step.ID, retry, 6)
	require.Error(t, err)
	balance, err := s.Balance(ctx, p)
	require.NoError(t, err)
	require.EqualValues(t, 500, balance.ReservedMicrocredits)
	_, err = s.DeferProviderRetry(ctx, run.ID, "worker", run.Fence, step.ID, retry, 6)
	require.NoError(t, err)
	balance, err = s.Balance(ctx, p)
	require.NoError(t, err)
	require.Zero(t, balance.ReservedMicrocredits)
	require.Zero(t, balance.SpentMicrocredits)
	current, err := s.ExecutionRun(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, domain.Queued, current.State)
	require.Greater(t, current.Fence, run.Fence)
	_, err = s.DeferProviderRetry(ctx, run.ID, "worker", run.Fence, step.ID, retry, 6)
	require.Error(t, err)
	persisted, err := s.ProviderRetry(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, 1, persisted.Attempts)
}

func TestProviderRetryClaimWaitsUntilEarlierOfScheduleAndDeadline(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ctx := t.Context()
	run, err := s.CreateRun(ctx, principal("alice"), "claim-wait", request())
	require.NoError(t, err)
	run, err = s.ClaimRun(ctx, run.ID, "worker", time.Minute)
	require.NoError(t, err)
	step, err := s.BeginStep(ctx, run.ID, "worker", run.Fence, "model", json.RawMessage(`{}`), 500)
	require.NoError(t, err)
	deadline := time.Now().Add(time.Minute)
	// First persist a schedulable rejection, then simulate a larger provider wait.
	_, err = s.DeferProviderRetry(
		ctx,
		run.ID,
		"worker",
		run.Fence,
		step.ID,
		domain.ProviderRetry{
			Attempts:      1,
			NextAttemptAt: time.Now().Add(time.Second),
			Deadline:      deadline,
			Code:          "rate_limit_exceeded",
		},
		6,
	)
	require.NoError(t, err)
	_, err = s.Pool().
		Exec(ctx, "UPDATE provider_retries SET next_attempt_at=deadline+interval '1 hour' WHERE run_id=$1", run.ID)
	require.NoError(t, err)
	_, err = s.ClaimRun(ctx, run.ID, "duplicate", time.Minute)
	var waiting *domain.ProviderRetryWaitError
	require.ErrorAs(t, err, &waiting)
	require.WithinDuration(t, deadline, waiting.Until, time.Microsecond)
	current, err := s.ExecutionRun(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, domain.Queued, current.State)
	require.Empty(t, current.LeaseOwner)
	require.Zero(t, current.ReservedMicrocredits)
}
