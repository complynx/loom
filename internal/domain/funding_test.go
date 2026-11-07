package domain_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
)

func TestFundingTargetFixedPrecision(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ base, global, personal, want int64 }{
		{1000000, 1000000, 1000000, 1000000},
		{1000000, 1500000, 2000000, 3000000},
		{3, 500000, 1000000, 1},
		{3, 500000, 2000000, 3},
		{100, 0, 1000000, 0},
	} {
		personal := tc.personal
		got, err := domain.FundingTarget(
			domain.MonthlyFundingDefaults{BaseMicrocredits: tc.base, FactorPPM: tc.global},
			domain.MonthlyFundingPolicy{FactorPPM: &personal},
		)
		require.NoError(t, err)
		require.Equal(t, tc.want, got)
	}
	override := int64(2000000)
	got, err := domain.FundingTarget(
		domain.MonthlyFundingDefaults{BaseMicrocredits: 1000000, FactorPPM: 3000000},
		domain.MonthlyFundingPolicy{BaseMicrocredits: &override},
	)
	require.NoError(t, err)
	require.EqualValues(t, 6000000, got)
	_, err = domain.FundingTarget(
		domain.MonthlyFundingDefaults{BaseMicrocredits: math.MaxInt64, FactorPPM: 2000000},
		domain.MonthlyFundingPolicy{},
	)
	require.ErrorContains(t, err, "monthly_funding_overflow")
	_, err = domain.FundingTarget(domain.MonthlyFundingDefaults{BaseMicrocredits: -1}, domain.MonthlyFundingPolicy{})
	require.Error(t, err)
}
