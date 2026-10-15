package config_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
)

func TestOTelConfigBounds(t *testing.T) {
	t.Parallel()
	cfg := config.OTel{Enabled: true, SampleRatio: .1}
	cfg.ApplyDefaults()
	require.NoError(t, cfg.Validate())
	for _, endpoint := range []string{"http://user:secret@alloy:4318", "http://alloy:4318?token=x", "http://alloy:4318/v1/traces", "file:///tmp/collector"} {
		bad := cfg
		bad.Endpoint = endpoint
		require.Error(t, bad.Validate())
	}
	for _, ratio := range []float64{-1, 2, math.NaN(), math.Inf(1)} {
		bad := cfg
		bad.SampleRatio = ratio
		require.Error(t, bad.Validate())
	}
}
