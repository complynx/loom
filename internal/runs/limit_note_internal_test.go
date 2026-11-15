package runs

import (
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

	retried := &turnState{}
	replayLimitNote(retried, step(provider.Response{ErrorCode: "provider_retry"}))
	require.False(t, retried.limitNoteSaved)
}
