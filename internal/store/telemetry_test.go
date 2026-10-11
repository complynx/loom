package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/clx-scripter/internal/app"
	"github.com/complynx/clx-scripter/internal/config"
	"github.com/complynx/clx-scripter/internal/domain"
)

func TestTelemetryTracksUnknownReserveAndRecovery(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	ctx := context.Background()
	require.NoError(t, app.Migrate(ctx, config.Config{Database: config.Database{DSN: s.Pool().Config().ConnString()}}))
	p := principal("private-user")
	run, err := s.CreateRun(ctx, p, "metrics-run", request())
	require.NoError(t, err)
	before, err := s.TelemetrySnapshot(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, before.Runs["queued"])
	run, err = s.ClaimRun(ctx, run.ID, "worker", time.Minute)
	require.NoError(t, err)
	step, err := s.BeginStep(
		ctx,
		run.ID,
		"worker",
		run.Fence,
		"model",
		json.RawMessage(`{"prompt":"private-input"}`),
		500,
	)
	require.NoError(t, err)
	require.NoError(t, s.MarkStepUnknown(ctx, run.ID, "worker", run.Fence, step.ID))
	after, err := s.TelemetrySnapshot(ctx)
	require.NoError(t, err)
	require.InDelta(t, 500, after.Gauges["clx_reserved_microcredits"], 0)
	require.InDelta(t, 1, after.Gauges["clx_unknown_inference_steps"], 0)
	require.Contains(t, after.Gauges, "clx_oldest_reservation_age_seconds")
	payload, err := json.Marshal(after)
	require.NoError(t, err)
	require.NotContains(t, string(payload), "private-user")
	require.NotContains(t, string(payload), "private-input")
	p.Permissions["operations.reconcile"] = true
	usage := domain.Usage{InputTokens: 10}
	usage.Microcredits, err = domain.UsageCost(usage, run.Model)
	require.NoError(t, err)
	_, err = s.ReconcileStep(
		ctx,
		p,
		run.ID,
		step.ID,
		"receipt",
		domain.ReconcileStepRequest{
			Usage: usage,
			Evidence: domain.ReconcileEvidence{
				Kind:      "provider_receipt",
				Reference: "verified-receipt",
				Reason:    "usage verified",
			},
		},
	)
	require.NoError(t, err)
	recovered, err := s.TelemetrySnapshot(ctx)
	require.NoError(t, err)
	require.Zero(t, recovered.Gauges["clx_reserved_microcredits"])
	require.Zero(t, recovered.Gauges["clx_unknown_inference_steps"])
}

func TestTelemetryQueueAgeStartsWhenAnswerRequeuesRun(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	ctx := context.Background()
	require.NoError(t, app.Migrate(ctx, config.Config{Database: config.Database{DSN: s.Pool().Config().ConnString()}}))
	p := principal("alice")
	run, err := s.CreateRun(ctx, p, "queue-age", request())
	require.NoError(t, err)
	// Simulate a run created long before the user answered its question.
	_, err = s.Pool().Exec(ctx, "UPDATE runs SET created_at=now()-interval '2 hours' WHERE id=$1", run.ID)
	require.NoError(t, err)
	run, err = s.ClaimRun(ctx, run.ID, "worker", time.Minute)
	require.NoError(t, err)
	require.NoError(
		t,
		s.AskQuestion(
			ctx,
			run.ID,
			"worker",
			run.Fence,
			domain.Question{ID: "choice", Text: "Choose", AllowText: true, ExpiresAt: time.Now().Add(time.Hour)},
		),
	)
	_, err = s.AddInput(
		ctx,
		p,
		run.ID,
		"answer",
		domain.RunInput{Kind: "answer", QuestionID: "choice", Text: "continue"},
	)
	require.NoError(t, err)
	snapshot, err := s.TelemetrySnapshot(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, snapshot.Runs["queued"])
	require.Less(t, snapshot.Gauges["clx_oldest_queued_run_age_seconds"], float64(30))
}
