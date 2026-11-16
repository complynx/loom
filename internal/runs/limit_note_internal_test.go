package runs

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/provider"
)

// After a restart, a saved note must not cause a second model call.
func TestReplayLimitNoteRestoresSavedNoteOnly(t *testing.T) {
	t.Parallel()
	step := func(response provider.Response) domain.Step {
		raw, err := json.Marshal(response)
		require.NoError(t, err)
		return domain.Step{Kind: limitNoteKind, Result: raw}
	}
	saved := &turnState{}
	replayLimitNote(saved, step(provider.Response{Text: "Half done. Continue?"}))
	require.True(t, saved.limitNoteSaved)
	require.Equal(t, "Half done. Continue?", saved.limitNote)

	failed := &turnState{}
	replayLimitNote(failed, step(provider.Response{Text: "unusable", ErrorCode: "limit_note_failed"}))
	require.True(t, failed.limitNoteSaved)
	require.Empty(t, failed.limitNote)
}

func TestLimitNoteUsesLowestAllowedEffort(t *testing.T) {
	t.Parallel()
	e := New(nil, nil, Options{})
	request, ready := e.limitNoteRequest(
		t.Context(),
		domain.Run{Effort: "high"},
		domain.Principal{},
		"",
		domain.Model{Efforts: []string{"low", "medium", "high"}, MaxOutputTokens: 4096},
		&turnState{},
	)
	require.True(t, ready)
	require.Equal(t, "low", request.Effort)
	require.Equal(t, 1024, request.MaxOutputTokens)
}

type noteTokenCounter struct{}

func (noteTokenCounter) Respond(context.Context, provider.Request) (provider.Response, error) {
	return provider.Response{}, nil
}

func (noteTokenCounter) CountInputTokens(context.Context, provider.Request) (int64, error) {
	return 9000, nil
}

func TestNoteInputBoundCountsOpaqueCompactionBeforeReservation(t *testing.T) {
	t.Parallel()
	e := New(nil, noteTokenCounter{}, Options{})
	request := provider.Request{
		MaxOutputTokens: 1024,
		Input:           []provider.Item{json.RawMessage(`{"type":"compaction","encrypted_content":"opaque"}`)},
	}
	model := domain.Model{ContextTokens: 20000, MaxOutputTokens: 1024}
	bound, err := e.requestInputBound(t.Context(), domain.Run{}, model, request, 100)
	require.NoError(t, err)
	require.EqualValues(t, 9000, bound)
	model.ContextTokens = 9500
	_, err = e.requestInputBound(t.Context(), domain.Run{}, model, request, 100)
	var suspension *Suspension
	require.ErrorAs(t, err, &suspension)
	require.Equal(t, "context_unavailable", suspension.Reason)
}

func TestNoteArtifactBoundUsesItsActualOutputAllowance(t *testing.T) {
	t.Parallel()
	e := New(nil, nil, Options{})
	run := domain.Run{Input: domain.Input{ArtifactIDs: []string{"image"}}}
	bound, err := e.requestInputBound(
		t.Context(),
		run,
		domain.Model{ContextTokens: 20000, MaxOutputTokens: 4096},
		provider.Request{MaxOutputTokens: 1024},
		100,
	)
	require.NoError(t, err)
	require.EqualValues(t, 18976, bound)
}
