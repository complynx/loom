package domain_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
)

func TestTokenBillingSeparatesCacheWritesAndLongContext(t *testing.T) {
	t.Parallel()
	model := domain.Model{
		InputMicrocreditsPerMillion:           100000,
		InputCachedMicrocreditsPerMillion:     10000,
		InputCacheWriteMicrocreditsPerMillion: 125000,
		OutputMicrocreditsPerMillion:          500000,
		LongContext: &domain.LongContextTariff{
			ThresholdTokens:                   272000,
			InputMicrocreditsPerMillion:       200000,
			CachedInputMicrocreditsPerMillion: 20000,
			CacheWriteMicrocreditsPerMillion:  250000,
			OutputMicrocreditsPerMillion:      750000,
		},
	}
	for _, test := range []struct {
		name  string
		usage domain.Usage
		want  int64
	}{
		{"mixed input", domain.Usage{InputTokens: 10000, CachedInputTokens: 2000, CacheWriteTokens: 3000, OutputTokens: 1000}, 1395},
		{"short boundary", domain.Usage{InputTokens: 272000, OutputTokens: 1000}, 27700},
		{"long boundary", domain.Usage{InputTokens: 272001, OutputTokens: 1000}, 55151},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cost, err := domain.UsageCost(test.usage, model)
			require.NoError(t, err)
			require.Equal(t, test.want, cost)
			reserve, err := domain.ReservationCost(test.usage.InputTokens, test.usage.OutputTokens, model)
			require.NoError(t, err)
			require.GreaterOrEqual(t, reserve, cost)
		})
	}
	_, err := domain.UsageCost(domain.Usage{InputTokens: 10, CachedInputTokens: 8, CacheWriteTokens: 3}, model)
	require.Error(t, err)
	_, err = domain.UsageCost(
		domain.Usage{InputTokens: math.MaxInt64},
		domain.Model{InputMicrocreditsPerMillion: math.MaxInt64},
	)
	require.Error(t, err)
}
