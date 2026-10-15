package config_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
)

func TestIdentityCacheConfiguration(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile("../../examples/config.yaml")
	require.NoError(t, err)
	for _, tc := range []struct {
		name, setting string
		ttl           int
		invalid       bool
	}{
		{"default", "", 120, false},
		{"disabled", "  permissions_cache_ttl_seconds: 0\n", 0, false},
		{"maximum", "  permissions_cache_ttl_seconds: 300\n", 300, false},
		{"negative", "  permissions_cache_ttl_seconds: -1\n", 0, true},
		{"too long", "  permissions_cache_ttl_seconds: 301\n", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			source := strings.Replace(
				strings.ReplaceAll(string(body), "\r\n", "\n"),
				"  permissions_cache_ttl_seconds: 120 # 0 disables; maximum 300. Sensitive writes bypass it.\n",
				tc.setting,
				1,
			)
			cfg, parseErr := config.Parse([]byte(source), func(k string) (string, bool) {
				if k == "CLX_MODEL_ENDPOINT" {
					return "https://models.example/v1", true
				}
				return "configured", true
			})
			if tc.invalid {
				require.Error(t, parseErr)
				return
			}
			require.NoError(t, parseErr)
			require.Equal(t, tc.ttl, cfg.Identity.PermissionsCacheTTLSeconds)
		})
	}
}
