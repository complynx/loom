package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
)

func TestTranscriptionReceiptUsesFrozenTariffAndSettlesOnce(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ctx := context.Background()
	p := principal("paid-tool")
	p.Permissions["operations.reconcile"] = true
	r, err := s.CreateRun(ctx, p, "run", request())
	require.NoError(t, err)
	r, err = s.ClaimRun(ctx, r.ID, "worker", time.Minute)
	require.NoError(t, err)
	intent := domain.PaidToolIntent{
		Call: domain.ToolCall{ID: "audio-call", Name: "audio_transcribe", Arguments: json.RawMessage(`{}`)},
		Billing: domain.PaidToolBillingSnapshot{
			Provider:            "fixture",
			Model:               "audio",
			Mode:                "duration",
			DurationMS:          2000,
			CreditsPerSecond:    3,
			ReserveMicrocredits: 6,
		},
	}
	payload, err := json.Marshal(intent)
	require.NoError(t, err)
	step, err := s.BeginStep(ctx, r.ID, "worker", r.Fence, "transcription", payload, 6)
	require.NoError(t, err)
	err = s.CompleteStep(
		ctx,
		r.ID,
		"worker",
		r.Fence,
		step.ID,
		json.RawMessage(`{"text":"hello"}`),
		domain.Usage{AudioDurationMS: 1001, Microcredits: 3},
	)
	code(t, err, "receipt_tariff_mismatch")
	require.NoError(t, s.MarkStepUnknown(ctx, r.ID, "worker", r.Fence, step.ID))
	balance, err := s.Balance(ctx, p)
	require.NoError(t, err)
	require.EqualValues(t, 6, balance.ReservedMicrocredits)
	require.Zero(t, balance.SpentMicrocredits)
	_, err = s.ClaimRun(ctx, r.ID, "another-worker", time.Minute)
	require.Error(t, err)
	receipt := domain.ReconcileStepRequest{
		Usage: domain.Usage{AudioDurationMS: 1001, Microcredits: 3},
		Evidence: domain.ReconcileEvidence{
			Kind:      "provider_receipt",
			Reference: "fixture-receipt",
			Reason:    "verified duration",
		},
		Result: json.RawMessage(`{"text":"hello"}`),
	}
	_, err = s.ReconcileStep(ctx, p, r.ID, step.ID, "bad", receipt)
	code(t, err, "receipt_tariff_mismatch")
	receipt.Usage.Microcredits = 4
	r, err = s.ReconcileStep(ctx, p, r.ID, step.ID, "receipt", receipt)
	require.NoError(t, err)
	require.Equal(t, domain.Paused, r.State)
	steps, err := s.ExecutionSteps(ctx, r.ID)
	require.NoError(t, err)
	require.Len(t, steps, 1)
	var result struct {
		Type   string `json:"type"`
		CallID string `json:"call_id"`
		Output string `json:"output"`
	}
	require.NoError(t, json.Unmarshal(steps[0].Result, &result))
	require.Equal(t, "function_call_output", result.Type)
	require.Equal(t, "audio-call", result.CallID)
	require.JSONEq(t, `{"text":"hello"}`, result.Output)

	_, err = s.ReconcileStep(ctx, p, r.ID, step.ID, "receipt", receipt)
	require.NoError(t, err)
	balance, err = s.Balance(ctx, p)
	require.NoError(t, err)
	require.EqualValues(t, 4, balance.SpentMicrocredits)
	require.Zero(t, balance.ReservedMicrocredits)
}
