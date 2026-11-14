package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/store"
)

func TestInputRacingStepLimitCheckpoint(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	r, err := s.CreateRun(t.Context(), principal("alice"), "create", request())
	require.NoError(t, err)
	r, err = s.ClaimRun(t.Context(), r.ID, "worker", time.Minute)
	require.NoError(t, err)
	for range r.SettingsSnapshot.MaxSteps {
		step, beginErr := s.BeginStep(t.Context(), r.ID, "worker", r.Fence, "tool", json.RawMessage(`{}`), 0)
		require.NoError(t, beginErr)
		err = s.CompleteStep(t.Context(), r.ID, "worker", r.Fence, step.ID, json.RawMessage(`{}`), domain.Usage{})
		require.NoError(t, err)
	}
	input := domain.RunInput{Kind: "steering", Text: "Continue"}
	_, err = s.AddInput(t.Context(), principal("alice"), r.ID, "continue", input)
	require.NoError(t, err)
	queued := 0
	s = store.New(s.Pool(), func(_ context.Context, _ pgx.Tx, _ string) error { queued++; return nil })
	// The executor made this decision before the new input committed.
	require.NoError(t, s.Transition(t.Context(), r.ID, "worker", r.Fence, domain.Paused, "step_limit", ""))
	fresh, err := s.ExecutionRun(t.Context(), r.ID)
	require.NoError(t, err)
	require.Equal(t, domain.Queued, fresh.State)
	require.Empty(t, fresh.Reason)
	require.Empty(t, fresh.LeaseOwner)
	require.Equal(t, 1, queued)
}

func TestSteeringPreservesOtherPauses(t *testing.T) {
	t.Parallel()
	for _, reason := range []string{"client_pause", "shutdown", "budget_exhausted"} {
		t.Run(reason, func(t *testing.T) {
			t.Parallel()
			s := testStore(t)
			r, err := s.CreateRun(t.Context(), principal("alice"), "create", request())
			require.NoError(t, err)
			r, err = s.ClaimRun(t.Context(), r.ID, "worker", time.Minute)
			require.NoError(t, err)
			require.NoError(t, s.Transition(t.Context(), r.ID, "worker", r.Fence, domain.Paused, reason, ""))
			input := domain.RunInput{Kind: "steering", Text: "Continue"}
			r, err = s.AddInput(t.Context(), principal("alice"), r.ID, "continue", input)
			require.NoError(t, err)
			require.Equal(t, domain.Paused, r.State)
			require.Equal(t, reason, r.Reason)
		})
	}
}
