package config_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
)

func TestActionLogConfiguration(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../examples/config.yaml")
	require.NoError(t, err)
	fixture := strings.ReplaceAll(string(raw), "\r\n", "\n")
	fixture = strings.Replace(fixture, "  action_log:\n    enabled: false", "  action_log:\n    enabled: true", 1)
	cfg, err := config.Parse([]byte(fixture), func(string) (string, bool) { return "configured", true })
	require.NoError(t, err)
	require.Equal(
		t,
		config.ActionLog{Enabled: true, Path: "/var/lib/loom/actions.jsonl", MaxSizeMB: 32, MaxBackups: 5},
		cfg.Observability.ActionLog,
	)
	for _, value := range []config.ActionLog{
		{Enabled: true}, {MaxSizeMB: -1}, {MaxSizeMB: 1025}, {MaxBackups: -1}, {MaxBackups: 101},
	} {
		require.Error(t, value.Validate())
	}
	require.NoError(t, (config.ActionLog{}).Validate())
}
