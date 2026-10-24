package runs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/provider"
)

func TestNativeFilesProfileDeniesUnexpectedToolsAndOmitsMemory(t *testing.T) {
	t.Parallel()
	run := domain.Run{ExecutionProfile: domain.ProfileNativeFiles}
	memory := &profileMemoryStore{}
	engine := &Engine{store: memory}
	for _, name := range []string{"module_call", "interaction_resolve", "message_react", "audio_transcribe"} {
		result, waiting, err := engine.executeCall(
			t.Context(),
			run,
			domain.Principal{},
			"",
			provider.Call{Name: name},
			domain.Settings{},
		)
		require.NoError(t, err)
		require.False(t, waiting)
		require.JSONEq(t, `{"error":"execution_profile_denied"}`, string(result))
	}
	instructions, err := engine.userMemoryInstructions(t.Context(), run, domain.Principal{}, "file task")
	require.NoError(t, err)
	require.Equal(t, "file task", instructions)
	require.Zero(t, memory.reads)
	_, err = engine.userMemoryInstructions(
		t.Context(),
		domain.Run{},
		domain.Principal{ServiceAccount: true},
		"service task",
	)
	require.NoError(t, err)
	require.Zero(t, memory.reads)
	_, err = engine.userMemoryInstructions(t.Context(), domain.Run{}, domain.Principal{}, "normal task")
	require.NoError(t, err)
	require.Equal(t, 1, memory.reads)
	engine.opts.Tools = func(context.Context, domain.Run, domain.Principal, string) ([]provider.Tool, error) {
		return []provider.Tool{{Name: "module_call"}, {Name: "audio_transcribe"}, {Name: "artifact_attach"}}, nil
	}
	tools, err := engine.turnTools(t.Context(), run, domain.Principal{}, "")
	require.NoError(t, err)
	for _, tool := range tools {
		require.Contains(t, []string{"script", "question", "artifact_attach"}, tool.Name)
	}
}

type profileMemoryStore struct {
	Store

	reads int
}

func (s *profileMemoryStore) ExecutionUserMemory(context.Context, string) (string, error) {
	s.reads++
	return "private memory", nil
}
