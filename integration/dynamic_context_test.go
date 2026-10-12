package integration_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
)

func TestDynamicContextReductionStopsNextInferenceWithoutCharging(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"live-reduction", "snapshot-ceiling"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			ctx := t.Context()
			f.repository.SetExecutionCeilings(
				5000,
				4,
				32768,
				domain.Settings{ScriptTimeoutMS: 5000, ParallelRPCLimit: 4, ContextInputTokenBudget: 32768},
			)
			admin := domain.Principal{
				Issuer:      "https://test.identity",
				Subject:     "admin",
				OrgID:       "test-org",
				Permissions: map[string]bool{"settings.write": true},
			}
			settings, err := f.repository.GetSettings(ctx)
			require.NoError(t, err)
			settings.ContextInputTokenBudget = 8000
			if change == "snapshot-ceiling" {
				settings.ContextInputTokenBudget = 1600
			}
			settings, err = f.repository.UpdateSettings(ctx, admin, settings.Version, settings)
			require.NoError(t, err)
			model, engine := fakeModel(t, f, func(int) any { return finalResponse("done") })
			run := f.createRun(t, runRequest("", "", strings.Repeat("x", 1000)), "dynamic-context")
			if change == "live-reduction" {
				settings.ContextInputTokenBudget = 1600
			} else {
				settings.ContextInputTokenBudget = 8000
			}
			_, err = f.repository.UpdateSettings(ctx, admin, settings.Version, settings)
			require.NoError(t, err)
			require.NoError(t, engine.Execute(ctx, run.ID))
			result := f.run(t, run.ID)
			require.Equal(t, domain.Paused, result.State)
			require.Equal(t, "context_unavailable", result.Reason)
			require.Zero(t, result.SpentMicrocredits)
			require.Zero(t, result.ReservedMicrocredits)
			model.mu.Lock()
			calls := len(model.requests)
			model.mu.Unlock()
			require.Zero(t, calls)
		})
	}
}
