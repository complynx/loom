package config_test

import (
	"os"
	"strings"
	"testing"

	"github.com/complynx/clx-scripter/internal/config"

	"github.com/stretchr/testify/require"
)

func TestOpenAIDeploymentDefaultsAndExplicitOverride(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../deploy/config.example.yaml")
	require.NoError(t, err)
	lookup := func(key string) (string, bool) {
		if key == "CLX_ZITADEL_ISSUER" {
			return "https://identity.example", true
		}
		return "test-only", true
	}
	defaults, err := config.Parse(raw, lookup)
	require.NoError(t, err)
	require.Equal(t, "https://api.openai.com/v1", defaults.Providers["openai"].Endpoint)
	require.Equal(t, "gpt-6-luna", defaults.BootstrapSettings.DefaultModelID)
	require.Equal(t, "medium", defaults.BootstrapSettings.DefaultEffort)
	require.Len(t, defaults.BootstrapSettings.ModelCatalog, 3)
	require.EqualValues(t, 244800, defaults.BootstrapSettings.ModelCatalog[0].CompactionThresholdTokens)
	require.EqualValues(t, 272000, defaults.BootstrapSettings.ModelCatalog[0].InputTokenLimit)
	require.EqualValues(t, 204000, defaults.BootstrapSettings.ModelCatalog[0].CompactionTargetTokens)
	require.Len(t, defaults.BootstrapSettings.Tariffs, 3)
	require.EqualValues(t, 125000, defaults.BootstrapSettings.Tariffs[0].CacheWriteCreditsPerMillion)
	override := strings.Replace(
		strings.ReplaceAll(string(raw), "\r\n", "\n"),
		"bootstrap_settings:\n",
		`bootstrap_settings:
  model_catalog:
    - {id: custom, provider: openai, model: custom-model, efforts: [low], max_effort: low, context_tokens: 8192, max_output_tokens: 100}
  default_model_id: custom
  default_effort: low
  tariffs:
    - {model_id: custom, input_credits_per_million: 123, output_credits_per_million: 456}
`,
		1,
	)
	explicit, err := config.Parse([]byte(override), lookup)
	require.NoError(t, err)
	require.Equal(t, "custom", explicit.BootstrapSettings.DefaultModelID)
	require.Len(t, explicit.BootstrapSettings.ModelCatalog, 1)
	require.EqualValues(t, 123, explicit.BootstrapSettings.Tariffs[0].InputCreditsPerMillion)
}

func TestModelCompactionLimitsAreValidated(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../examples/config.yaml")
	require.NoError(t, err)
	configuration, err := config.Parse(raw, func(string) (string, bool) { return "test-only", true })
	require.NoError(t, err)
	configuration.BootstrapSettings.ModelCatalog[0].CompactionThresholdTokens = 120000
	configuration.BootstrapSettings.ModelCatalog[0].CompactionTargetTokens = 90000
	require.NoError(t, configuration.Validate())
	configuration.BootstrapSettings.ModelCatalog[0].CompactionTargetTokens = 120000
	require.Error(t, configuration.Validate())
}
