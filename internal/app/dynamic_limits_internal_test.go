package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/modules"
	"github.com/complynx/loom/internal/sandbox"
)

type deadlineExecutor struct{ remaining time.Duration }

func (e *deadlineExecutor) Execute(
	ctx context.Context,
	_ string,
	_ []sandbox.Binding,
	_ sandbox.Broker,
) (json.RawMessage, error) {
	deadline, _ := ctx.Deadline()
	e.remaining = time.Until(deadline)
	return json.RawMessage(`{"ok":true}`), nil
}

func TestScriptUsesLiveReductionAndKeepsAdmittedCeiling(t *testing.T) {
	t.Parallel()
	repository, _ := searchToolStore(t)
	repository.SetExecutionCeilings(
		5000,
		4,
		32768,
		domain.Settings{ScriptTimeoutMS: 5000, ParallelRPCLimit: 4, ContextInputTokenBudget: 32768},
	)
	p := domain.Principal{
		Subject:     "alice",
		Actor:       "alice",
		OrgID:       "org",
		Permissions: map[string]bool{"chat.read": true, "settings.write": true},
	}
	settings, err := repository.GetSettings(t.Context())
	require.NoError(t, err)
	run := domain.Run{SettingsSnapshot: settings}
	executor := &deadlineExecutor{}
	tools := runtimeTools{
		repository: repository,
		registry:   &modules.Registry{},
		executor:   executor,
		cfg:        config.Config{Sandbox: config.Sandbox{ScriptTimeoutCeilingMS: 5000, ParallelRPCCeiling: 4}},
		authorize:  func(context.Context, domain.Run) (domain.Principal, string, error) { return p, "", nil },
	}
	// The test store deliberately has minimal bootstrap settings. Populate the existing editable fields before updating it.
	settings.ActiveRunLimit = 4
	settings.HistoryRecentLimit = 50
	settings.MaxSteps = 10
	settings.QuestionTimeoutSeconds = 60
	settings.ScriptTimeoutMS = 100
	settings, err = repository.UpdateSettings(t.Context(), p, settings.Version, settings)
	require.NoError(t, err)
	_, err = tools.executeScript(t.Context(), run, "return true;")
	require.NoError(t, err)
	require.Positive(t, executor.remaining)
	require.LessOrEqual(t, executor.remaining, 100*time.Millisecond)
	run.SettingsSnapshot.ScriptTimeoutMS = 50
	settings.ScriptTimeoutMS = 5000
	_, err = repository.UpdateSettings(t.Context(), p, settings.Version, settings)
	require.NoError(t, err)
	_, err = tools.executeScript(t.Context(), run, "return true;")
	require.NoError(t, err)
	require.Positive(t, executor.remaining)
	require.LessOrEqual(t, executor.remaining, 50*time.Millisecond)
}
