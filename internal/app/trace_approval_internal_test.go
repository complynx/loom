package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/provider"
	"github.com/complynx/loom/internal/runs"
	"github.com/complynx/loom/internal/store"
	"github.com/complynx/loom/internal/telemetry"
)

// Consent and the effect endpoint are fixtures; PostgreSQL, River and engine receipts are real.
func TestApprovalContinuationUsesConsumedTraceAndDoesNotRepeatEffect(t *testing.T) {
	t.Parallel()
	initial, pool := searchToolStore(t)
	c := newQueueTraceReceiver(t)
	o := c.observer(t)
	p := domain.Principal{
		Issuer:      "https://issuer.test",
		Subject:     "alice",
		Actor:       "alice",
		OrgID:       "org",
		Permissions: map[string]bool{"runs.create": true, "runs.control": true, "chat.read": true},
	}
	origin := telemetry.TraceContext{Parent: traceOrigin}
	ctx := trace.ContextWithRemoteSpanContext(t.Context(), origin.SpanContext())
	run, err := initial.CreateRun(
		ctx,
		p,
		"approval-run",
		domain.CreateRun{
			Input:       domain.Input{Text: "perform the approved fixture effect"},
			Interaction: domain.Interaction{Approvals: true},
		},
	)
	require.NoError(t, err)
	run, err = initial.ClaimRun(ctx, run.ID, "fixture", time.Minute)
	require.NoError(t, err)
	approval := domain.Approval{
		ID:          uuid.NewString(),
		OperationID: uuid.NewString(),
		ReplyMode:   domain.ReplyModeProtocol,
		ExpiresAt:   time.Now().Add(time.Minute),
		Call:        &domain.ToolCall{ID: "fixture-effect", Name: "module_call", Arguments: json.RawMessage(`{}`)},
	}
	require.NoError(t, initial.AskApproval(ctx, run.ID, "fixture", run.Fence, approval))
	initial.SetApprovalHandler(func(context.Context, pgx.Tx, domain.Run, domain.RunInput) error { return nil })
	input := domain.RunInput{Kind: domain.InputApproval, ApprovalID: approval.ID, Decision: domain.DecisionApprove}
	inputID, consent := tracedInput(
		t,
		o,
		initial,
		p,
		run.ID,
		"approve",
		"00-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-bbbbbbbbbbbbbbbb-01",
		input,
	)
	replayID, _ := tracedInput(
		t,
		o,
		initial,
		p,
		run.ID,
		"approve",
		"00-cccccccccccccccccccccccccccccccc-cccccccccccccccc-01",
		input,
	)
	require.Equal(t, inputID, replayID)
	_, _, before, err := initial.RunTrace(t.Context(), run.ID)
	require.NoError(t, err)
	require.Empty(t, before)
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	require.NoError(t, err)
	_, err = migrator.Migrate(t.Context(), rivermigrate.DirectionUp, nil)
	require.NoError(t, err)
	var effects atomic.Int32
	seen := make(chan telemetry.TraceContext, 2)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/effect" {
			effects.Add(1)
			seen <- telemetry.TraceContext{Parent: r.Header.Get("Traceparent"), State: r.Header.Get("Tracestate")}
			_, _ = w.Write([]byte(`{"status":"succeeded"}`))
			return
		}
		if r.URL.Path == "/responses/input_tokens" {
			_, _ = w.Write([]byte(`{"input_tokens":10}`))
			return
		}
		_, _ = w.Write(
			[]byte(
				`{"id":"done","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Completed"}]}],"usage":{"input_tokens":10,"output_tokens":2}}`,
			),
		)
	}))
	t.Cleanup(peer.Close)
	httpClient := &http.Client{Transport: telemetry.TraceTransport{}}
	model, err := provider.NewResponses(peer.URL, "test-only", httpClient)
	require.NoError(t, err)
	var queue *river.Client[pgx.Tx]
	repository := store.New(
		pool,
		func(ctx context.Context, tx pgx.Tx, id string) error { return insertRunJob(ctx, tx, queue, id) },
	)
	engine := runs.New(repository, model, runs.Options{
		TraceStep: executionTraceStep(repository, o),
		Authorize: func(context.Context, domain.Run) (domain.Principal, string, error) { return p, "", nil },
		ModuleCall: func(ctx context.Context, current domain.Run, _ domain.Principal, _ string, _ provider.Call) (json.RawMessage, *domain.Approval, error) {
			if current.Approval == nil || current.Approval.Decision != domain.DecisionApprove {
				return nil, nil, domain.Forbidden()
			}
			request, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, peer.URL+"/effect", nil)
			if requestErr != nil {
				return nil, nil, requestErr
			}
			request.Header.Set("Idempotency-Key", current.Approval.OperationID)
			response, requestErr := httpClient.Do(request)
			if requestErr != nil {
				return nil, nil, requestErr
			}
			_ = response.Body.Close()
			return json.RawMessage(`{"status":"succeeded"}`), nil, nil
		},
	})
	queue, _, err = runtimeQueue(config.Config{}, pool, engine, repository, o, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	tx, err := pool.Begin(t.Context())
	require.NoError(t, err)
	require.NoError(t, insertRunJob(t.Context(), tx, queue, run.ID))
	require.NoError(t, tx.Commit(t.Context()))
	require.NoError(t, queue.Start(t.Context()))
	t.Cleanup(func() { _ = queue.StopAndCancel(context.Background()) })
	require.Eventually(t, func() bool {
		r, e := repository.ExecutionRun(t.Context(), run.ID)
		return e == nil && r.State == domain.Completed
	}, 10*time.Second, 20*time.Millisecond)
	require.NoError(t, queue.Stop(t.Context()))
	worker := runWorker{engine: engine, repository: repository, observer: o}
	require.NoError(t, worker.Work(t.Context(), &river.Job[executeRun]{Args: executeRun{RunID: run.ID}}))
	require.EqualValues(t, 1, effects.Load())
	var effect telemetry.TraceContext
	select {
	case effect = <-seen:
	default:
		t.Fatal("effect was not dispatched")
	}
	require.Equal(t, consent.SpanContext().TraceID(), effect.SpanContext().TraceID())
	require.Eventually(t, func() bool { return c.span(effect) != nil }, 3*time.Second, 20*time.Millisecond)
	requireTraceLink(t, c.span(effect), origin)
	requireTraceLink(t, c.span(effect), consent)
	_, _, applied, err := store.New(pool, nil).RunTrace(t.Context(), run.ID)
	require.NoError(t, err)
	require.Equal(t, consent, applied)
}
