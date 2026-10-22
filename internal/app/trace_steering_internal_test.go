package app

import (
	"context"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/stretchr/testify/require"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/provider"
	"github.com/complynx/loom/internal/runs"
	"github.com/complynx/loom/internal/store"
	"github.com/complynx/loom/internal/telemetry"
)

func tracedInput(
	t *testing.T,
	observer *telemetry.Telemetry,
	repository *store.Store,
	p domain.Principal,
	runID, key, parent string,
	in domain.RunInput,
) (string, telemetry.TraceContext) {
	t.Helper()
	var receipt domain.Run
	var carrier telemetry.TraceContext
	var err error
	mux := http.NewServeMux()
	mux.HandleFunc("POST /input", func(_ http.ResponseWriter, r *http.Request) {
		carrier = telemetry.CaptureTrace(r.Context())
		receipt, err = repository.AddInput(r.Context(), p, runID, key, in)
	})
	request := httptest.NewRequest(http.MethodPost, "/input", nil)
	request.Header.Set("Traceparent", parent)
	request.Header.Set("Baggage", "secret=do-not-store")
	observer.HTTP(mux).ServeHTTP(httptest.NewRecorder(), request)
	require.NoError(t, err)
	require.NotNil(t, receipt.ConversationInput)
	return receipt.ConversationInput.ID, carrier
}

func (c *queueTraceReceiver) span(carrier telemetry.TraceContext) *tracepb.Span {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, span := range c.spans {
		if hex.EncodeToString(span.GetSpanId()) == carrier.SpanContext().SpanID().String() &&
			hex.EncodeToString(span.GetTraceId()) == carrier.SpanContext().TraceID().String() {
			return span
		}
	}
	return nil
}

func requireTraceLink(t *testing.T, span *tracepb.Span, carrier telemetry.TraceContext) {
	t.Helper()
	for _, link := range span.GetLinks() {
		if hex.EncodeToString(link.GetTraceId()) == carrier.SpanContext().TraceID().String() &&
			hex.EncodeToString(link.GetSpanId()) == carrier.SpanContext().SpanID().String() {
			return
		}
	}
	t.Fatalf("missing causal link to %s/%s", carrier.SpanContext().TraceID(), carrier.SpanContext().SpanID())
}

func TestSteeringTraceChangesOnlyWhenInputsAreConsumed(t *testing.T) {
	t.Parallel()
	c := newQueueTraceReceiver(t)
	o := c.observer(t)
	var f *callerRecoveryFixture
	mux := http.NewServeMux()
	mux.HandleFunc("POST /accept", func(_ http.ResponseWriter, r *http.Request) {
		//nolint:contextcheck // Fixture resources use the test lifetime; only acceptance uses the request lifetime.
		f = newCallerRecoveryFixture(t, r.Context())
	})
	request := httptest.NewRequest(http.MethodPost, "/accept", nil)
	request.Header.Set("Traceparent", traceOrigin)
	o.HTTP(mux).ServeHTTP(httptest.NewRecorder(), request)
	pool := f.repository.Pool()
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	require.NoError(t, err)
	_, err = migrator.Migrate(t.Context(), rivermigrate.DirectionUp, nil)
	require.NoError(t, err)
	_, err = pool.Exec(
		t.Context(),
		"UPDATE runs SET state='queued',lease_owner='',lease_until=NULL WHERE id=$1",
		f.run.ID,
	)
	require.NoError(t, err)
	var queue *river.Client[pgx.Tx]
	repository := store.New(
		pool,
		func(ctx context.Context, tx pgx.Tx, id string) error { return insertRunJob(ctx, tx, queue, id) },
	)
	seen := make(chan telemetry.TraceContext, 2)
	release := make(chan struct{})
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/responses/input_tokens" {
			_, _ = w.Write([]byte(`{"input_tokens":10}`))
			return
		}
		seen <- telemetry.TraceContext{Parent: r.Header.Get("Traceparent"), State: r.Header.Get("Tracestate")}
		if calls.Add(1) == 1 {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = w.Write(
			[]byte(
				`{"id":"result","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Completed"}]}],"usage":{"input_tokens":10,"output_tokens":2}}`,
			),
		)
	}))
	t.Cleanup(model.Close)
	client, err := provider.NewResponses(model.URL, "test-only", &http.Client{Transport: telemetry.TraceTransport{}})
	require.NoError(t, err)
	engine := runs.New(
		repository,
		client,
		runs.Options{
			TraceStep: executionTraceStep(repository, o),
			Authorize: func(context.Context, domain.Run) (domain.Principal, string, error) { return f.principal, "", nil },
		},
	)
	queue, _, err = runtimeQueue(config.Config{}, pool, engine, repository, o, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	tx, err := pool.Begin(t.Context())
	require.NoError(t, err)
	require.NoError(t, insertRunJob(t.Context(), tx, queue, f.run.ID))
	require.NoError(t, tx.Commit(t.Context()))
	require.NoError(t, queue.Start(t.Context()))
	t.Cleanup(func() { _ = queue.StopAndCancel(context.Background()) })
	var first telemetry.TraceContext
	select {
	case first = <-seen:
	case <-time.After(10 * time.Second):
		t.Fatal("initial inference did not start")
	}
	origin, _, _, err := repository.RunTrace(t.Context(), f.run.ID)
	require.NoError(t, err)
	require.Equal(t, origin.SpanContext().TraceID(), first.SpanContext().TraceID())
	bID, b := tracedInput(
		t,
		o,
		repository,
		f.principal,
		f.run.ID,
		"B",
		"00-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-bbbbbbbbbbbbbbbb-01",
		domain.RunInput{Kind: domain.InputSteering, Text: "first correction"},
	)
	cID, latest := tracedInput(
		t,
		o,
		repository,
		f.principal,
		f.run.ID,
		"C",
		"00-cccccccccccccccccccccccccccccccc-cccccccccccccccc-01",
		domain.RunInput{Kind: domain.InputSteering, Text: "second correction"},
	)
	_, _, applied, err := repository.RunTrace(t.Context(), f.run.ID)
	require.NoError(t, err)
	require.Empty(t, applied)
	close(release)
	var subsequent telemetry.TraceContext
	select {
	case subsequent = <-seen:
	case <-time.After(10 * time.Second):
		t.Fatal("consuming inference did not start")
	}
	require.Equal(t, latest.SpanContext().TraceID(), subsequent.SpanContext().TraceID())
	require.NotEqual(t, first.SpanContext().TraceID(), subsequent.SpanContext().TraceID())
	require.Eventually(t, func() bool {
		r, e := repository.ExecutionRun(t.Context(), f.run.ID)
		return e == nil && r.State == domain.Completed
	}, 10*time.Second, 20*time.Millisecond)
	require.NoError(t, queue.Stop(t.Context()))
	require.Eventually(t, func() bool { return c.span(subsequent) != nil }, 3*time.Second, 20*time.Millisecond)
	span := c.span(subsequent)
	require.Equal(t, latest.SpanContext().SpanID().String(), hex.EncodeToString(span.GetParentSpanId()))
	requireTraceLink(t, span, origin)
	requireTraceLink(t, span, b)
	requireTraceLink(t, span, latest)
	var count int
	require.NoError(
		t,
		pool.QueryRow(t.Context(), "SELECT count(*) FROM run_inputs WHERE run_id=$1 AND id=ANY($2::uuid[]) AND applied", f.run.ID, []string{bID, cID}).
			Scan(&count),
	)
	require.Equal(t, 2, count)
	require.EqualValues(t, 2, calls.Load())
}
