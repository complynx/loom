package config_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/clx-scripter/internal/config"
)

func TestProviderRetryConfigurationBounds(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile("../../examples/config.yaml")
	require.NoError(t, err)
	cfg, err := config.Parse(source, func(key string) (string, bool) {
		if key == "CLX_MODEL_ENDPOINT" {
			return "https://models.example/v1", true
		}
		return "configured", true
	})
	require.NoError(t, err)
	require.Equal(t, 6, cfg.Execution.ProviderRetryAttemptLimit)
	require.Equal(t, 2, cfg.Execution.ProviderRetryShortLimit)
	for _, edit := range []func(*config.Execution){
		func(e *config.Execution) { e.ProviderRetryAttemptLimit = 21 },
		func(e *config.Execution) { e.ProviderRetryWindowSeconds = 0 },
		func(e *config.Execution) { e.ProviderRetryShortLimit = 3 },
		func(e *config.Execution) { e.ProviderRetryShortBudgetMS = 3001 },
	} {
		candidate := cfg
		edit(&candidate.Execution)
		require.Error(t, candidate.Validate())
	}
	cfg.Execution.ProviderRetryAttemptLimit = 0
	cfg.Execution.ProviderRetryShortLimit = 0
	cfg.Execution.ProviderRetryWindowSeconds = 0
	require.NoError(t, cfg.Validate())
}
