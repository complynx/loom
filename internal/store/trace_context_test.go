package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/store"
	"github.com/complynx/loom/internal/telemetry"
)

func TestTraceContextAcceptanceReplayAndLease(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	origin := telemetry.DecodeTrace([]byte(`{"traceparent":"00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"}`))
	ctx := trace.ContextWithRemoteSpanContext(t.Context(), origin.SpanContext())
	run, err := s.CreateRun(ctx, principal("alice"), "traced", request())
	require.NoError(t, err)
	_, err = s.CreateRun(t.Context(), principal("alice"), "traced", request())
	require.NoError(t, err)
	got, previous, input, err := s.RunTrace(t.Context(), run.ID)
	require.NoError(t, err)
	require.Equal(t, origin, got)
	require.Empty(t, previous)
	require.Empty(t, input)
	run, err = s.ClaimRun(ctx, run.ID, "owner", time.Minute)
	require.NoError(t, err)
	_, err = s.ClaimRun(t.Context(), run.ID, "stale", time.Minute)
	require.Error(t, err)
	_, previous, _, err = store.New(s.Pool(), nil).RunTrace(t.Context(), run.ID)
	require.NoError(t, err)
	require.Equal(t, origin, previous)
	_, err = s.AddInput(
		ctx,
		principal("alice"),
		run.ID,
		"steer",
		domain.RunInput{Kind: domain.InputSteering, Text: "continue"},
	)
	require.NoError(t, err)
	_, err = s.AddInput(
		context.WithoutCancel(t.Context()),
		principal("alice"),
		run.ID,
		"steer",
		domain.RunInput{Kind: domain.InputSteering, Text: "continue"},
	)
	require.NoError(t, err)
	_, _, input, err = s.RunTrace(t.Context(), run.ID)
	require.NoError(t, err)
	require.Empty(t, input)
	pending, err := s.PendingInputs(ctx, run.ID, "owner", run.Fence)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.NoError(t, s.MarkInputsApplied(ctx, run.ID, "owner", run.Fence, []string{pending[0].ID}))
	_, _, input, err = s.RunTrace(t.Context(), run.ID)
	require.NoError(t, err)
	require.Equal(t, origin, input)
}

func TestAppliedTraceIsMonotonicAcrossRestartAndDelayedInput(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	run, err := s.CreateRun(t.Context(), principal("alice"), "run", request())
	require.NoError(t, err)
	run, err = s.ClaimRun(t.Context(), run.ID, "worker", time.Minute)
	require.NoError(t, err)
	old := telemetry.TraceContext{Parent: "00-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-bbbbbbbbbbbbbbbb-01"}
	newer := telemetry.TraceContext{Parent: "00-cccccccccccccccccccccccccccccccc-cccccccccccccccc-01"}
	add := func(key string, c telemetry.TraceContext) string {
		t.Helper()
		ctx := trace.ContextWithRemoteSpanContext(t.Context(), c.SpanContext())
		receipt, addErr := s.AddInput(
			ctx,
			principal("alice"),
			run.ID,
			key,
			domain.RunInput{Kind: domain.InputSteering, Text: key},
		)
		require.NoError(t, addErr)
		return receipt.ConversationInput.ID
	}
	oldID := add("old", old)
	newID := add("new", newer)
	s = store.New(s.Pool(), nil)
	_, _, before, err := s.RunTrace(t.Context(), run.ID)
	require.NoError(t, err)
	require.Empty(t, before)
	require.NoError(t, s.MarkInputsApplied(t.Context(), run.ID, "worker", run.Fence, []string{newID}))
	s = store.New(s.Pool(), nil)
	require.NoError(t, s.MarkInputsApplied(t.Context(), run.ID, "worker", run.Fence, []string{oldID}))
	_, _, after, err := s.RunTrace(t.Context(), run.ID)
	require.NoError(t, err)
	require.Equal(t, newer, after)
	snapshot, err := s.ExecutionTrace(t.Context(), run.ID)
	require.NoError(t, err)
	require.Equal(t, []telemetry.TraceContext{old}, snapshot.Inputs)
	require.NoError(t, s.MarkInputsApplied(t.Context(), run.ID, "worker", run.Fence, []string{newID, oldID}))
	replay, err := s.ExecutionTrace(t.Context(), run.ID)
	require.NoError(t, err)
	require.Equal(t, snapshot, replay)
	require.Equal(t, newID, add("new", old), "idempotent replay retains the original input carrier")
	staleID := add("stale", old)
	require.Error(t, s.MarkInputsApplied(t.Context(), run.ID, "worker", run.Fence-1, []string{staleID}))
	var applied bool
	require.NoError(
		t,
		s.Pool().QueryRow(t.Context(), "SELECT applied FROM run_inputs WHERE id=$1", staleID).Scan(&applied),
	)
	require.False(t, applied)
}
