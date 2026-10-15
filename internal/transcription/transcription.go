// Package transcription streams audio to providers and preserves their billing receipts.
package transcription

import (
	"context"
	"errors"
	"io"
	"math"
	"math/big"
	"strconv"
)

// Provider converts an audio stream into a transcript without automatic retries.
type Provider interface {
	Transcribe(context.Context, Request) (Result, error)
}

// Request references a bounded stream. DurationSeconds is metadata, not a billing receipt.
type Request struct {
	Model, Filename, MIME, Language, Prompt string
	ClientRequestID                         string
	Audio                                   io.Reader
	DurationSeconds                         float64
}

// Segment is a provider-supplied timestamp, when supported by the selected model.
type Segment struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

// Usage retains the provider's explicit units. Missing receipts must not be estimated here.
type Usage struct {
	Type            string  `json:"type"`
	InputTokens     int64   `json:"input_tokens"`
	AudioTokens     int64   `json:"audio_tokens"`
	TextTokens      int64   `json:"text_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	DurationSeconds float64 `json:"duration_seconds"`
}

// Result is bounded text and metadata; audio bytes are never returned.
type Result struct {
	Text      string    `json:"text"`
	Segments  []Segment `json:"segments,omitempty"`
	Usage     Usage     `json:"usage"`
	RequestID string    `json:"request_id"`
}

// FailureError identifies whether a durable intent needs reconciliation.
type FailureError struct {
	Kind       string
	StatusCode int
	RequestID  string
	Reason     string
}

// Error excludes provider bodies, credentials, and request URLs.
func (e *FailureError) Error() string { return "transcription " + e.Kind + ": " + e.Reason }

const (
	FailurePreDispatch    = "pre_dispatch"
	FailureUnknown        = "unknown"
	FailureRejected       = "provider_rejected"
	UsageTokens           = "tokens"
	UsageDuration         = "duration"
	tokensPerMillion      = 1_000_000
	millisecondsPerSecond = 1000
)

// Tariff uses integer credits per million tokens or a rational credits-per-second rate.
type Tariff struct {
	AudioPerMillion, TextPerMillion, OutputPerMillion int64
	DurationNumerator, DurationDenominator            int64
}

// Cost rounds the aggregate charge upward once and rejects missing units or overflow.
func Cost(usage Usage, tariff Tariff) (int64, error) {
	if tariff.AudioPerMillion < 0 || tariff.TextPerMillion < 0 || tariff.OutputPerMillion < 0 ||
		tariff.DurationNumerator < 0 || tariff.DurationDenominator < 0 {
		return 0, errors.New("negative transcription tariff")
	}
	charge := new(big.Rat)
	switch usage.Type {
	case UsageTokens:
		if usage.InputTokens < 0 || usage.AudioTokens < 0 || usage.TextTokens < 0 || usage.OutputTokens < 0 ||
			usage.AudioTokens > usage.InputTokens || usage.TextTokens != usage.InputTokens-usage.AudioTokens {
			return 0, errors.New("invalid transcription token receipt")
		}
		counts := []int64{usage.AudioTokens, usage.TextTokens, usage.OutputTokens}
		rates := []int64{tariff.AudioPerMillion, tariff.TextPerMillion, tariff.OutputPerMillion}
		for index, count := range counts {
			product := new(big.Int).Mul(big.NewInt(count), big.NewInt(rates[index]))
			charge.Add(charge, new(big.Rat).SetFrac(product, big.NewInt(tokensPerMillion)))
		}
	case UsageDuration:
		if !finite(usage.DurationSeconds) || usage.DurationSeconds < 0 || tariff.DurationDenominator <= 0 {
			return 0, errors.New("invalid transcription duration receipt or tariff")
		}
		charge.SetString(strconv.FormatFloat(usage.DurationSeconds, 'f', -1, 64))
		charge.Mul(
			charge,
			new(big.Rat).SetFrac(big.NewInt(tariff.DurationNumerator), big.NewInt(tariff.DurationDenominator)),
		)
	default:
		return 0, errors.New("missing or unknown transcription usage units")
	}
	quotient, remainder := new(big.Int).QuoRem(charge.Num(), charge.Denom(), new(big.Int))
	if remainder.Sign() > 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() {
		return 0, errors.New("transcription credit overflow")
	}
	return quotient.Int64(), nil
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

// DurationMilliseconds rounds a decimal receipt up once without binary-float artifacts.
func DurationMilliseconds(seconds float64) (int64, error) {
	if !finite(seconds) || seconds < 0 {
		return 0, errors.New("invalid transcription duration")
	}
	value, _ := new(big.Rat).SetString(strconv.FormatFloat(seconds, 'f', -1, 64))
	value.Mul(value, big.NewRat(millisecondsPerSecond, 1))
	quotient, remainder := new(big.Int).QuoRem(value.Num(), value.Denom(), new(big.Int))
	if remainder.Sign() > 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() {
		return 0, errors.New("transcription duration overflow")
	}
	return quotient.Int64(), nil
}
