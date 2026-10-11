package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/clx-scripter/internal/domain"
	"github.com/complynx/clx-scripter/internal/runs"
)

func TestScriptAuthorizationDenialProvesNoExecutionStarted(t *testing.T) {
	t.Parallel()
	denial := &runs.Suspension{State: domain.Paused, Reason: "disclosure_denied"}
	tools := runtimeTools{authorize: func(context.Context, domain.Run) (domain.Principal, string, error) {
		return domain.Principal{}, "", denial
	}}
	// No executor or registry is configured: admission must return before either is used.
	_, err := tools.executeScript(t.Context(), domain.Run{}, "return 1")
	var notStarted *runs.ScriptNotStartedError
	require.ErrorAs(t, err, &notStarted)
	require.ErrorIs(t, err, denial)
}
