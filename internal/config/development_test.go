package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
)

func TestDevelopmentTransportKeepsProductionCatalog(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../deploy/config.example.yaml")
	require.NoError(t, err)
	text := string(raw)
	start := strings.Index(text, "providers:")
	end := strings.Index(text[start:], "artifacts:") + start
	require.Greater(t, end, start)
	for _, kind := range []string{"mock", "codex"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			block := "providers:\n  openai:\n    kind: " + kind + "\n"
			if kind == "mock" {
				block += "    mailbox: '" + filepath.Join(dir, "mailbox") + "'\n"
			} else {
				block += "    executable: '" + filepath.Join(
					dir,
					"codex",
				) + "'\n    codex_home: '" + filepath.Join(
					dir,
					"profile",
				) + "'\n"
			}
			c, parseErr := config.Parse([]byte(text[:start]+block+text[end:]), func(key string) (string, bool) {
				if key == "CLX_ZITADEL_ISSUER" {
					return "https://identity.example", true
				}
				return "test-value", true
			})
			require.NoError(t, parseErr)
			require.Equal(t, "gpt-6-luna", c.BootstrapSettings.DefaultModelID)
			require.EqualValues(t, 125000, c.BootstrapSettings.Tariffs[0].CacheWriteCreditsPerMillion)
			require.Len(t, c.BootstrapSettings.ModelCatalog, 3)
			require.Equal(t, []string{"text"}, c.BootstrapSettings.ModelCatalog[0].InputModalities)
			c.Providers["openai"] = config.Provider{Kind: kind}
			require.Error(t, c.Validate())
		})
	}
}
