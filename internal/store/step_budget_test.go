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

func TestInputRenewalWhileLimitNoteIsPending(t *testing.T) {
	t.Parallel()
	for _, during := range []bool{false, true} {
		t.Run(
			map[bool]string{false: "before_note", true: "during_note"}[during],
			func(t *testing.T) { t.Parallel(); checkInputRenewalAtNoteBoundary(t, during) },
		)
	}
}

func checkInputRenewalAtNoteBoundary(t *testing.T, during bool) {
	t.Helper()
	s := testStore(t)
	p := principal("pending-note-input")
	_, err := s.Pool().Exec(t.Context(), `UPDATE settings SET doc=jsonb_set(doc,'{max_steps}','1')`)
	require.NoError(t, err)
	r, err := s.CreateRun(t.Context(), p, "create", request())
	require.NoError(t, err)
	r, err = s.ClaimRun(t.Context(), r.ID, "worker", time.Minute)
	require.NoError(t, err)
	step, err := s.BeginStep(t.Context(), r.ID, "worker", r.Fence, "model", json.RawMessage(`{}`), 0)
	require.NoError(t, err)
	require.NoError(
		t,
		s.CompleteStep(t.Context(), r.ID, "worker", r.Fence, step.ID, json.RawMessage(`{}`), domain.Usage{}),
	)
	var note domain.Step
	if during {
		note, err = s.BeginStep(
			t.Context(),
			r.ID,
			"worker",
			r.Fence,
			domain.LimitNoteStepKind,
			json.RawMessage(`{"reason":"step_limit"}`),
			0,
		)
		require.NoError(t, err)
	}
	receipt, err := s.RecoverChatMessage(
		t.Context(),
		p,
		r.ChatID,
		"continue",
		domain.RecoverMessage{Text: "New direction"},
	)
	require.NoError(t, err)
	require.True(t, receipt.Delivered)
	if during {
		require.NoError(
			t,
			s.CompleteStep(t.Context(), r.ID, "worker", r.Fence, note.ID, json.RawMessage(`{}`), domain.Usage{}),
		)
	} else {
		_, err = s.BeginStep(
			t.Context(),
			r.ID,
			"worker",
			r.Fence,
			domain.LimitNoteStepKind,
			json.RawMessage(`{"reason":"step_limit"}`),
			0,
		)
		code(t, err, "step_budget_renewed")
	}
	require.NoError(t, s.Transition(t.Context(), r.ID, "worker", r.Fence, domain.Paused, "step_limit", "Earlier note"))
	current, err := s.GetRun(t.Context(), p, r.ID)
	require.NoError(t, err)
	require.Equal(t, domain.Queued, current.State)
	require.Empty(t, current.Result)
	current, err = s.ClaimRun(t.Context(), r.ID, "new-worker", time.Minute)
	require.NoError(t, err)
	inputs, err := s.PendingInputs(t.Context(), r.ID, "new-worker", current.Fence)
	require.NoError(t, err)
	require.Len(t, inputs, 1)
	require.Equal(t, receipt.InputID, inputs[0].ID)
	applied, err := s.BeginStep(t.Context(), r.ID, "new-worker", current.Fence, "input", json.RawMessage(`{}`), 0)
	require.NoError(t, err)
	require.NoError(
		t,
		s.CompleteStep(
			t.Context(),
			r.ID,
			"new-worker",
			current.Fence,
			applied.ID,
			json.RawMessage(`{"role":"user","content":"New direction"}`),
			domain.Usage{},
		),
	)
	require.NoError(t, s.MarkInputsApplied(t.Context(), r.ID, "new-worker", current.Fence, []string{inputs[0].ID}))
	remaining, err := s.PendingInputs(t.Context(), r.ID, "new-worker", current.Fence)
	require.NoError(t, err)
	require.Empty(t, remaining)
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
