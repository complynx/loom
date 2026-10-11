package config_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/clx-scripter/internal/config"
)

func TestConfigurationRejectsInvalidDefaults(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../examples/config.yaml")
	require.NoError(t, err)
	cfg, err := config.Parse(raw, func(key string) (string, bool) {
		if key == "CLX_MODEL_ENDPOINT" {
			return "https://models.example/v1", true
		}
		return "configured", true
	})
	require.NoError(t, err)
	cfg.BootstrapSettings.DefaultEffort = "unsupported"
	require.ErrorContains(t, cfg.Validate(), "default effort")
	cfg.BootstrapSettings.DefaultModelID = ""
	require.ErrorContains(t, cfg.Validate(), "default model")
}
