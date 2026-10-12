package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/provider"
	"github.com/complynx/loom/internal/runs"
)

type audioReceiptFixture struct {
	calls atomic.Int64
	err   error
	after func()
}

func (*audioReceiptFixture) Prepare(
	_ context.Context,
	_ domain.Run,
	_ domain.Principal,
	_ string,
	call provider.Call,
) (domain.PaidToolIntent, int64, error) {
	return domain.PaidToolIntent{
		Call: domain.ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments},
		Billing: domain.PaidToolBillingSnapshot{
			Provider:            "fixture",
			Model:               "audio",
			Mode:                "duration",
			DurationMS:          2000,
			CreditsPerSecond:    75,
			ReserveMicrocredits: 150,
		},
	}, 150, nil
}

func (a *audioReceiptFixture) Execute(
	context.Context,
	domain.Run,
	domain.Principal,
	string,
	domain.PaidToolIntent,
	string,
) (json.RawMessage, domain.Usage, error) {
	a.calls.Add(1)
	if a.after != nil {
		a.after()
	}
	return json.RawMessage(
		`{"text":"receipt survives restart"}`,
	), domain.Usage{
		AudioDurationMS: 1001,
		RequestID:       "audio-receipt",
	}, a.err
}

func TestPaidAudioDispatchSettlesOrRetainsHoldWithoutRestartReplay(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"success", "rejected", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			t.Parallel()
			checkPaidAudioOutcome(t, outcome)
		})
	}
}

func audioEngineFactory(
	t *testing.T,
	f *fixture,
	model *modelFixture,
	paid *audioReceiptFixture,
	replay ...func(context.Context, domain.Run, domain.PaidToolIntent) error,
) func(string) *runs.Engine {
	t.Helper()
	client, err := provider.NewResponses(model.server.URL, "test-provider-key", model.server.Client())
	require.NoError(t, err)
	var replayHook func(context.Context, domain.Run, domain.PaidToolIntent) error
	if len(replay) > 0 {
		replayHook = replay[0]
	}
	return func(owner string) *runs.Engine {
		return runs.New(f.repository, client, runs.Options{Owner: owner, PaidTool: paid, PaidToolReplay: replayHook,
			Tools: func(context.Context, domain.Run, domain.Principal, string) ([]provider.Tool, error) {
				return []provider.Tool{
					{
						Type:        "function",
						Name:        "audio_transcribe",
						Description: "fixture audio",
						Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
					},
				}, nil
			},
			Authorize: func(ctx context.Context, run domain.Run) (domain.Principal, string, error) {
				req, authErr := http.NewRequestWithContext(ctx, http.MethodGet, "http://boundary", nil)
				if authErr != nil {
					return domain.Principal{}, "", authErr
				}
				req.Header.Set("Authorization", "Bearer "+run.ActorID)
				p, authErr := (testAuth{}).Authenticate(ctx, req)
				return p, "token", authErr
			},
		})
	}
}

func checkPaidAudioOutcome(t *testing.T, outcome string) {
	t.Helper()
	f := newFixture(t)
	model, _ := fakeModel(t, f, func(index int) any {
		if index > 1 {
			return finalResponse("done")
		}
		return map[string]any{
			"id":     "audio-turn",
			"status": "completed",
			"output": []any{
				map[string]any{
					"type":      "function_call",
					"call_id":   "audio-call",
					"name":      "audio_transcribe",
					"arguments": `{}`,
				},
			},
			"usage": map[string]int{"input_tokens": 10, "output_tokens": 5},
		}
	})
	paid := &audioReceiptFixture{}
	if outcome == "rejected" {
		paid.err = &runs.PaidToolError{Disposition: "rejected", Cause: errors.New("provider rejected")}
	}
	if outcome == "unknown" {
		paid.err = errors.New("connection lost after dispatch")
	}
	makeEngine := audioEngineFactory(t, f, model, paid)
	run := f.createRun(t, runRequest("", "", "transcribe"), "audio-run")
	require.NoError(t, makeEngine("audio-worker").Execute(t.Context(), run.ID))
	got := f.run(t, run.ID)
	if outcome == "unknown" {
		require.Equal(t, domain.Reconciling, got.State)
		require.EqualValues(t, 150, got.ReservedMicrocredits)
		require.EqualValues(t, 15, got.SpentMicrocredits)
	} else {
		require.Equal(t, domain.Completed, got.State)
		require.Zero(t, got.ReservedMicrocredits)
		spent := int64(30)
		if outcome == "success" {
			spent += 76
		}
		require.Equal(t, spent, got.SpentMicrocredits)
	}
	_ = makeEngine("restarted-audio-worker").Execute(t.Context(), run.ID)
	require.EqualValues(t, 1, paid.calls.Load())
	if outcome == "unknown" {
		unresolved, err := f.repository.UnresolvedSteps(t.Context(), run.ID)
		require.NoError(t, err)
		require.Len(t, unresolved, 1)
		reconcileAudioAndResume(t, f, run.ID, unresolved[0].ID, makeEngine, paid, model)
	} else {
		steps, err := f.repository.ExecutionSteps(t.Context(), run.ID)
		require.NoError(t, err)
		count := 0
		for _, step := range steps {
			if step.Kind == "transcription" {
				count++
			}
		}
		require.Equal(t, 1, count)
	}
	model.mu.Lock()
	require.Contains(t, string(model.requests[0]), `"name":"audio_transcribe"`)
	model.mu.Unlock()
}

func TestSavedTranscriptionReceiptReplaysAfterPauseWithoutPaidRedispatch(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	model, _ := fakeModel(t, f, func(index int) any {
		if index > 1 {
			return finalResponse("continued transcript")
		}
		return map[string]any{
			"id":     "audio-turn",
			"status": "completed",
			"output": []any{
				map[string]any{
					"type":      "function_call",
					"call_id":   "audio-call",
					"name":      "audio_transcribe",
					"arguments": `{}`,
				},
			},
			"usage": map[string]int{"input_tokens": 10, "output_tokens": 5},
		}
	})
	run := f.createRun(t, runRequest("", "", "transcribe and continue"), "audio-resume")
	paid := &audioReceiptFixture{after: func() {
		code, raw := f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/pause", "pause-after-audio", nil)
		require.Equal(t, 200, code, string(raw))
	}}
	var replayAllowed atomic.Bool
	replayAllowed.Store(true)
	makeEngine := audioEngineFactory(t, f, model, paid, func(context.Context, domain.Run, domain.PaidToolIntent) error {
		if !replayAllowed.Load() {
			return domain.Forbidden()
		}
		return nil
	})
	require.NoError(t, makeEngine("original-audio-worker").Execute(t.Context(), run.ID))
	paused := f.run(t, run.ID)
	require.Equal(t, domain.Paused, paused.State)
	require.EqualValues(t, 91, paused.SpentMicrocredits)
	require.Zero(t, paused.ReservedMicrocredits)
	replayAllowed.Store(false)
	code, raw := f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/resume", "resume-revoked-audio", nil)
	require.Equal(t, 200, code, string(raw))
	require.NoError(t, makeEngine("revoked-audio-worker").Execute(t.Context(), run.ID))
	denied := f.run(t, run.ID)
	require.Equal(t, domain.Paused, denied.State)
	require.Equal(t, "artifact_disclosure_denied", denied.Reason)
	require.EqualValues(t, 91, denied.SpentMicrocredits)
	require.EqualValues(t, 1, paid.calls.Load())
	model.mu.Lock()
	require.Len(t, model.requests, 1)
	model.mu.Unlock()
	replayAllowed.Store(true)
	code, raw = f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/resume", "resume-audio", nil)
	require.Equal(t, 200, code, string(raw))
	require.NoError(t, makeEngine("new-audio-worker").Execute(t.Context(), run.ID))
	got := f.run(t, run.ID)
	require.Equal(t, domain.Completed, got.State)
	require.EqualValues(t, 106, got.SpentMicrocredits)
	require.EqualValues(t, 1, paid.calls.Load())
	model.mu.Lock()
	require.Len(t, model.requests, 2)
	require.Contains(t, string(model.requests[1]), "receipt survives restart")
	model.mu.Unlock()
}

func reconcileAudioAndResume(
	t *testing.T,
	f *fixture,
	runID, stepID string,
	makeEngine func(string) *runs.Engine,
	paid *audioReceiptFixture,
	model *modelFixture,
) {
	t.Helper()
	req := http.Request{Header: make(http.Header)}
	req.Header.Set("Authorization", "Bearer alice")
	p, err := (testAuth{}).Authenticate(t.Context(), &req)
	require.NoError(t, err)
	p.Permissions["operations.reconcile"] = true
	reconciled, err := f.repository.ReconcileStep(
		t.Context(),
		p,
		runID,
		stepID,
		"manual-audio-receipt",
		domain.ReconcileStepRequest{
			Usage: domain.Usage{AudioDurationMS: 1001, Microcredits: 76, RequestID: "audio-receipt"},
			Evidence: domain.ReconcileEvidence{
				Kind:      "provider_receipt",
				Reference: "audio-receipt",
				Reason:    "verified fixture duration",
			},
			Result: json.RawMessage(`{"text":"verified transcript after uncertainty"}`),
		},
	)
	require.NoError(t, err)
	require.Equal(t, domain.Paused, reconciled.State)
	code, raw := f.request(t, "alice", "POST", "/v1/runs/"+runID+"/resume", "resume-reconciled-audio", nil)
	require.Equal(t, 200, code, string(raw))
	require.NoError(t, makeEngine("reconciled-audio-worker").Execute(t.Context(), runID))
	got := f.run(t, runID)
	require.Equal(t, domain.Completed, got.State)
	require.EqualValues(t, 106, got.SpentMicrocredits)
	require.Zero(t, got.ReservedMicrocredits)
	require.EqualValues(t, 1, paid.calls.Load())
	model.mu.Lock()
	require.Len(t, model.requests, 2)
	require.Contains(t, string(model.requests[1]), "verified transcript after uncertainty")
	require.Contains(t, string(model.requests[1]), "function_call_output")
	model.mu.Unlock()
}
