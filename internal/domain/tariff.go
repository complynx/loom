package domain

import (
	"errors"
	"math/big"
	"net/http"
)

const tokensPerMillion = 1000000

// ValidateTariff checks rates without changing legacy tariffs that lack a long band.
func ValidateTariff(m Model) error {
	if m.InputMicrocreditsPerMillion < 0 || m.InputCachedMicrocreditsPerMillion < 0 ||
		m.InputCacheWriteMicrocreditsPerMillion < 0 ||
		m.OutputMicrocreditsPerMillion < 0 {
		return NewError(http.StatusUnprocessableEntity, "invalid_tariff")
	}
	if t := m.LongContext; t != nil &&
		(t.ThresholdTokens <= 0 || t.InputMicrocreditsPerMillion < 0 || t.CachedInputMicrocreditsPerMillion < 0 || t.CacheWriteMicrocreditsPerMillion < 0 || t.OutputMicrocreditsPerMillion < 0) {
		return NewError(http.StatusUnprocessableEntity, "invalid_tariff")
	}
	return nil
}

func tokenRates(input int64, m Model) (int64, int64, int64, int64) {
	if t := m.LongContext; t != nil && input > t.ThresholdTokens {
		return t.InputMicrocreditsPerMillion, t.CachedInputMicrocreditsPerMillion, t.CacheWriteMicrocreditsPerMillion, t.OutputMicrocreditsPerMillion
	}
	return m.InputMicrocreditsPerMillion, m.InputCachedMicrocreditsPerMillion, m.InputCacheWriteMicrocreditsPerMillion, m.OutputMicrocreditsPerMillion
}

// UsageCost applies the immutable tariff, rounding each input/output charge up.
// Cache reads and writes are disjoint subsets of total input, not additive fees.
func UsageCost(u Usage, m Model) (int64, error) {
	if u.InputTokens < 0 || u.CachedInputTokens < 0 || u.CachedInputTokens > u.InputTokens || u.CacheWriteTokens < 0 ||
		u.CacheWriteTokens > u.InputTokens-u.CachedInputTokens ||
		u.OutputTokens < 0 {
		return 0, NewError(http.StatusUnprocessableEntity, "invalid_usage")
	}
	if err := ValidateTariff(m); err != nil {
		return 0, err
	}
	ordinary, cached, written, outputRate := tokenRates(u.InputTokens, m)
	if u.CacheWriteTokens > 0 && written == 0 && ordinary > 0 {
		return 0, NewError(http.StatusUnprocessableEntity, "cache_write_tariff_missing")
	}
	input := new(big.Int).Mul(big.NewInt(u.InputTokens-u.CachedInputTokens-u.CacheWriteTokens), big.NewInt(ordinary))
	input.Add(input, new(big.Int).Mul(big.NewInt(u.CachedInputTokens), big.NewInt(cached)))
	input.Add(input, new(big.Int).Mul(big.NewInt(u.CacheWriteTokens), big.NewInt(written)))
	output := new(big.Int).Mul(big.NewInt(u.OutputTokens), big.NewInt(outputRate))
	return roundedCost(input, output)
}

// ReservationCost covers the most expensive possible input category and band.
func ReservationCost(input, output int64, m Model) (int64, error) {
	if input < 0 || output < 0 {
		return 0, NewError(http.StatusUnprocessableEntity, "invalid_usage")
	}
	if err := ValidateTariff(m); err != nil {
		return 0, err
	}
	ordinary, cached, written, outputRate := tokenRates(input, m)
	inputRate := max(ordinary, cached, written)
	// A conservative input bound may cross a band. Cover either band's rates.
	if m.LongContext != nil && input > m.LongContext.ThresholdTokens {
		inputRate = max(
			inputRate,
			m.InputMicrocreditsPerMillion,
			m.InputCachedMicrocreditsPerMillion,
			m.InputCacheWriteMicrocreditsPerMillion,
		)
		outputRate = max(outputRate, m.OutputMicrocreditsPerMillion)
	}
	return roundedCost(
		new(big.Int).Mul(big.NewInt(input), big.NewInt(inputRate)),
		new(big.Int).Mul(big.NewInt(output), big.NewInt(outputRate)),
	)
}

func roundedCost(input, output *big.Int) (int64, error) {
	for _, n := range []*big.Int{input, output} {
		n.Add(n, big.NewInt(tokensPerMillion-1))
		n.Quo(n, big.NewInt(tokensPerMillion))
	}
	input.Add(input, output)
	if !input.IsInt64() {
		return 0, NewError(http.StatusUnprocessableEntity, "tariff_overflow")
	}
	return input.Int64(), nil
}

// ValidateCompaction checks optional per-model input ceilings and summary targets.
func ValidateCompaction(m Model) error {
	if m.InputTokenLimit < 0 ||
		(m.InputTokenLimit > 0 && ((m.ContextTokens > 0 && m.InputTokenLimit > m.ContextTokens) || m.CompactionThresholdTokens > m.InputTokenLimit)) {
		return errors.New("invalid model input token limit")
	}
	if m.CompactionThresholdTokens < 0 || m.CompactionTargetTokens < 0 ||
		(m.CompactionThresholdTokens > 0 && (m.ContextTokens > 0 && m.CompactionThresholdTokens > m.ContextTokens || m.CompactionTargetTokens >= m.CompactionThresholdTokens)) ||
		(m.CompactionThresholdTokens == 0 && m.CompactionTargetTokens != 0) {
		return NewError(http.StatusUnprocessableEntity, "invalid_model_compaction")
	}
	return nil
}
