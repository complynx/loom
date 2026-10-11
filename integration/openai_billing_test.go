package integration_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
)

func TestAgentSettlesCacheWritesExactlyOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	settings, err := f.repository.GetSettings(context.Background())
	require.NoError(t, err)
	settings.Models[0].InputCachedMicrocreditsPerMillion = 100000
	settings.Models[0].InputCacheWriteMicrocreditsPerMillion = 2000000
	admin := domain.Principal{
		Issuer:      "https://test.identity",
		Subject:     "admin",
		OrgID:       "test-org",
		Permissions: map[string]bool{"settings.write": true},
	}
	_, err = f.repository.UpdateSettings(context.Background(), admin, settings.Version, settings)
	require.NoError(t, err)
	_, engine := fakeModel(t, f, func(int) any {
		response := finalResponse("done").(map[string]any)
		response["usage"] = map[string]any{
			"input_tokens":         20,
			"output_tokens":        5,
			"input_tokens_details": map[string]int64{"cached_tokens": 5, "cache_write_tokens": 10},
		}
		return response
	})
	run := f.createRun(t, runRequest("", "", "test billing"), "cache-writes")
	require.NoError(t, engine.Execute(context.Background(), run.ID))
	final := f.run(t, run.ID)
	require.Equal(t, domain.Completed, final.State)
	require.EqualValues(t, 31, final.SpentMicrocredits)
	require.Zero(t, final.ReservedMicrocredits)
	require.Error(t, engine.Execute(context.Background(), run.ID))
	require.EqualValues(t, 31, f.run(t, run.ID).SpentMicrocredits)
	steps, err := f.repository.ExecutionSteps(context.Background(), run.ID)
	require.NoError(t, err)
	require.Len(t, steps, 1)
	require.EqualValues(t, 10, steps[0].Usage.CacheWriteTokens)
}
