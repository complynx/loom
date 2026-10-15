package domain_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
)

func TestPaidToolDurationReceiptRoundsUpAndRejectsOverflow(t *testing.T) {
	t.Parallel()
	b := domain.PaidToolBillingSnapshot{
		Provider:            "fixture",
		Model:               "audio",
		Mode:                "duration",
		DurationMS:          2000,
		CreditsPerSecond:    3,
		ReserveMicrocredits: 6,
	}
	cost, err := domain.PaidToolCost(domain.Usage{AudioDurationMS: 1001}, b)
	require.NoError(t, err)
	require.EqualValues(t, 4, cost)
	_, err = domain.PaidToolCost(domain.Usage{AudioDurationMS: 2001}, b)
	require.Error(t, err)
	b.DurationMS, b.CreditsPerSecond = math.MaxInt64, math.MaxInt64
	_, err = domain.PaidToolCost(domain.Usage{AudioDurationMS: math.MaxInt64}, b)
	require.Error(t, err)
}

func TestPaidToolTokenReceiptUsesOneCombinedCeiling(t *testing.T) {
	t.Parallel()
	b := domain.PaidToolBillingSnapshot{
		Provider:                    "fixture",
		Model:                       "audio",
		Mode:                        "tokens",
		DurationMS:                  2000,
		AudioInputCreditsPerMillion: 1,
		TextInputCreditsPerMillion:  1,
		OutputCreditsPerMillion:     1,
		ReserveMicrocredits:         10,
	}
	u := domain.Usage{AudioInputTokens: 1, TextInputTokens: 1, InputTokens: 2, OutputTokens: 1}
	cost, err := domain.PaidToolCost(u, b)
	require.NoError(t, err)
	require.EqualValues(t, 1, cost)
	u.InputTokens = 3
	_, err = domain.PaidToolCost(u, b)
	require.Error(t, err)
}
