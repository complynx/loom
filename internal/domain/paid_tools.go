package domain

import (
	"encoding/json"
	"math/big"
	"net/http"
)

const paidTokenTariffScale = 1000000

// PaidToolBillingSnapshot freezes the tariff before a paid operation starts.
type PaidToolBillingSnapshot struct {
	Provider                    string `json:"provider"`
	Model                       string `json:"model"`
	Mode                        string `json:"mode"`
	DurationMS                  int64  `json:"duration_ms"`
	CreditsPerSecond            int64  `json:"credits_per_second"`
	AudioInputCreditsPerMillion int64  `json:"audio_input_credits_per_million"`
	TextInputCreditsPerMillion  int64  `json:"text_input_credits_per_million"`
	OutputCreditsPerMillion     int64  `json:"output_credits_per_million"`
	ReserveMicrocredits         int64  `json:"reserve_microcredits"`
}

// PaidToolIntent stores a validated call and its trusted billing snapshot.
type PaidToolIntent struct {
	Call     ToolCall                `json:"call"`
	Billing  PaidToolBillingSnapshot `json:"billing"`
	Prepared json.RawMessage         `json:"prepared,omitempty"`
}

func (b PaidToolBillingSnapshot) Validate() error {
	if b.Provider == "" || b.Model == "" || (b.Mode != "duration" && b.Mode != "tokens") ||
		b.DurationMS <= 0 || b.ReserveMicrocredits < 0 || b.CreditsPerSecond < 0 ||
		b.AudioInputCreditsPerMillion < 0 || b.TextInputCreditsPerMillion < 0 || b.OutputCreditsPerMillion < 0 {
		return NewError(http.StatusUnprocessableEntity, "invalid_paid_tool_tariff")
	}
	return nil
}

// PaidToolCost rounds the complete receipt up once, without integer overflow.
func PaidToolCost(u Usage, b PaidToolBillingSnapshot) (int64, error) {
	if err := b.Validate(); err != nil {
		return 0, err
	}
	if u.AudioDurationMS < 0 || u.AudioDurationMS > b.DurationMS || u.AudioInputTokens < 0 ||
		u.TextInputTokens < 0 || u.InputTokens < 0 || u.OutputTokens < 0 || u.CachedInputTokens != 0 || u.CacheWriteTokens != 0 {
		return 0, NewError(http.StatusUnprocessableEntity, "invalid_paid_tool_usage")
	}
	var numerator big.Int
	denominator := int64(paidTokenTariffScale)
	add := func(count, rate int64) {
		var product big.Int
		product.Mul(big.NewInt(count), big.NewInt(rate))
		numerator.Add(&numerator, &product)
	}
	if b.Mode == "duration" {
		if u.AudioInputTokens != 0 || u.TextInputTokens != 0 || u.InputTokens != 0 || u.OutputTokens != 0 {
			return 0, NewError(http.StatusUnprocessableEntity, "invalid_paid_tool_usage")
		}
		denominator = 1000
		add(u.AudioDurationMS, b.CreditsPerSecond)
	} else {
		var total big.Int
		total.Add(big.NewInt(u.AudioInputTokens), big.NewInt(u.TextInputTokens))
		if !total.IsInt64() || total.Int64() != u.InputTokens {
			return 0, NewError(http.StatusUnprocessableEntity, "invalid_paid_tool_usage")
		}
		add(u.AudioInputTokens, b.AudioInputCreditsPerMillion)
		add(u.TextInputTokens, b.TextInputCreditsPerMillion)
		add(u.OutputTokens, b.OutputCreditsPerMillion)
	}
	numerator.Add(&numerator, big.NewInt(denominator-1))
	numerator.Quo(&numerator, big.NewInt(denominator))
	if !numerator.IsInt64() {
		return 0, NewError(http.StatusUnprocessableEntity, "paid_tool_cost_overflow")
	}
	return numerator.Int64(), nil
}
