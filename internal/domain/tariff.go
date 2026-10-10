package domain

import (
	"math/big"
)

// UsageCost applies the immutable tariff, rounding each input/output charge up.
func UsageCost(u Usage, m Model) (int64, error) {
	if u.InputTokens < 0 || u.CachedInputTokens < 0 || u.CachedInputTokens > u.InputTokens || u.OutputTokens < 0 || m.InputMicrocreditsPerMillion < 0 || m.InputCachedMicrocreditsPerMillion < 0 || m.OutputMicrocreditsPerMillion < 0 {
		return 0, NewError(422, "invalid_usage")
	}
	input := new(big.Int).Mul(big.NewInt(u.InputTokens-u.CachedInputTokens), big.NewInt(m.InputMicrocreditsPerMillion))
	input.Add(input, new(big.Int).Mul(big.NewInt(u.CachedInputTokens), big.NewInt(m.InputCachedMicrocreditsPerMillion)))
	output := new(big.Int).Mul(big.NewInt(u.OutputTokens), big.NewInt(m.OutputMicrocreditsPerMillion))
	for _, n := range []*big.Int{input, output} {
		n.Add(n, big.NewInt(999999))
		n.Quo(n, big.NewInt(1000000))
	}
	input.Add(input, output)
	if !input.IsInt64() {
		return 0, NewError(422, "tariff_overflow")
	}
	return input.Int64(), nil
}
