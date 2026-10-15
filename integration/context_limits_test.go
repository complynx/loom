package integration_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
)

func TestModelContextCeilingPausesBeforePaidInference(t *testing.T) {
	t.Parallel()
	for _, ceiling := range []int64{1600, 6000} {
		t.Run(fmt.Sprintf("limit-%d", ceiling), func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			ctx := context.Background()
			settings, err := f.repository.GetSettings(ctx)
			require.NoError(t, err)
			settings.Models[0].CompactionThresholdTokens = ceiling
			settings.Models[0].InputTokenLimit = 7000
			settings.Models[0].CompactionTargetTokens = 500
			admin := domain.Principal{
				Issuer:      "https://test.identity",
				Subject:     "admin",
				OrgID:       "test-org",
				Permissions: map[string]bool{"settings.write": true},
			}
			_, err = f.repository.UpdateSettings(ctx, admin, settings.Version, settings)
			require.NoError(t, err)
			model, engine := fakeModel(t, f, func(int) any { return finalResponse("done") })
			run := f.createRun(t, runRequest("", "", strings.Repeat("x", 1000)), "context-ceiling")
			require.NoError(t, engine.Execute(ctx, run.ID))
			result := f.run(t, run.ID)
			model.mu.Lock()
			calls := len(model.requests)
			model.mu.Unlock()
			if ceiling == 1600 {
				require.Equal(t, domain.Paused, result.State)
				require.Zero(t, calls)
				require.Zero(t, result.SpentMicrocredits)
				require.Zero(t, result.ReservedMicrocredits)
			} else {
				require.Equal(t, domain.Completed, result.State)
				require.Equal(t, 1, calls)
			}
		})
	}
}
