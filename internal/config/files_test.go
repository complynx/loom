package config_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
)

func TestTranscriptionDefaultsAndRejectsUnsupportedBilling(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../../examples/config.yaml")
	require.NoError(t, err)
	baseline, _, found := strings.Cut(string(data), "# Optional private streamed file worker")
	require.True(t, found)
	body := baseline + "\nfile_tools:\n  enabled: true\ntranscription:\n  enabled: true\n"
	lookup := func(string) (string, bool) { return "configured", true }
	cfg, err := config.Parse([]byte(body), lookup)
	require.NoError(t, err)
	require.Equal(t, "gpt-transcribe", cfg.Transcription.Model)
	require.EqualValues(t, 75, cfg.Transcription.CreditsPerSecond)
	require.Equal(t, cfg.Providers["openai"].APIKey, cfg.Transcription.APIKey)
	require.Equal(t, "/run/loom-files/processor.sock", cfg.FileTools.Socket)
	_, err = config.Parse([]byte(body+"  model: gpt-4o-mini-transcribe\n  credits_per_second: 75\n"), lookup)
	require.Error(t, err)
}
