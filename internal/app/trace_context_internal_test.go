package app

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/stretchr/testify/require"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/modules"
	"github.com/complynx/loom/internal/provider"
	"github.com/complynx/loom/internal/runs"
	"github.com/complynx/loom/internal/store"
	"github.com/complynx/loom/internal/telemetry"
)

const traceOrigin = "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"

type queueTraceReceiver struct {
	mu     sync.Mutex
	spans  []*tracepb.Span
	server *httptest.Server
}

func newQueueTraceReceiver(t *testing.T) *queueTraceReceiver {
	t.Helper()
	c := &queueTraceReceiver{}
	c.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/traces" {
			raw, _ := io.ReadAll(r.Body)
			var export collectortrace.ExportTraceServiceRequest
			if proto.Unmarshal(raw, &export) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			c.mu.Lock()
			for _, resource := range export.GetResourceSpans() {
				for _, scope := range resource.GetScopeSpans() {
					c.spans = append(c.spans, scope.GetSpans()...)
				}
			}
			c.mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(c.server.Close)
	return c
}

func (c *queueTraceReceiver) observer(t *testing.T) *telemetry.Telemetry {
	t.Helper()
	o := telemetry.New(slog.New(slog.DiscardHandler))
	cfg := config.OTel{Enabled: true, Endpoint: c.server.URL, SampleRatio: 1, TimeoutMS: 100, MaxBatch: 1}
	cfg.ApplyDefaults()
	shutdown, err := o.EnableOTel(t.Context(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, shutdown(context.Background())) })
	return o
}

func (c *queueTraceReceiver) executions() []*tracepb.Span {
	c.mu.Lock()
	defer c.mu.Unlock()
	var found []*tracepb.Span
	for _, span := range c.spans {
		if span.GetName() == "run_execution" {
			found = append(found, span)
		}
	}
	return found
}

func queueTraceModule(t *testing.T) (*modules.Registry, <-chan telemetry.TraceContext) {
	t.Helper()
	raw, err := os.ReadFile("../../examples/manifest.json")
	require.NoError(t, err)
	var manifest modules.Manifest
	require.NoError(t, json.Unmarshal(raw, &manifest))
	manifest.Caller = nil
	manifest.Modules = manifest.Modules[:1]
	manifest.Modules[0].Methods = manifest.Modules[0].Methods[:1]
	seen := make(chan telemetry.TraceContext, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manifest" {
			_ = json.NewEncoder(w).Encode(manifest)
			return
		}
		seen <- telemetry.TraceContext{Parent: r.Header.Get("Traceparent"), State: r.Header.Get("Tracestate")}
		var request struct {
			OperationID string `json:"operation_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		_ = json.NewEncoder(w).
			Encode(map[string]any{"status": "succeeded", "operation_id": request.OperationID, "result": map[string]any{"slots": []any{}}})
	}))
	t.Cleanup(server.Close)
	address, err := url.Parse(server.URL)
	require.NoError(t, err)
	registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		return []modules.Endpoint{
			{
				ContainerID: "trace-module",
				URI:         "http://registered:" + address.Port() + "/manifest",
				Address:     "127.0.0.1",
			},
		}, nil
	}), modules.Options{WrapTransport: func(base http.RoundTripper) http.RoundTripper { return telemetry.TraceTransport{Base: base} }})
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(t.Context()))
	return registry, seen
}

// The request has ended before River starts. Later input does not replace its origin.
func TestQueuedTraceSurvivesGracefulRestartAndSteering(t *testing.T) {
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
	request.Header.Set("Tracestate", "qa=origin")
	request.Header.Set("Baggage", "secret=do-not-store")
	o.HTTP(mux).ServeHTTP(httptest.NewRecorder(), request)
	pool := f.repository.Pool()
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	require.NoError(t, err)
	_, err = migrator.Migrate(t.Context(), rivermigrate.DirectionUp, nil)
	require.NoError(t, err)
	var queue *river.Client[pgx.Tx]
	repository := store.New(
		pool,
		func(ctx context.Context, tx pgx.Tx, id string) error { return insertRunJob(ctx, tx, queue, id) },
	)
	// Fixture creation claims a lease for unrelated caller tests. Release it before queue delivery.
	_, err = pool.Exec(
		t.Context(),
		"UPDATE runs SET state='queued',lease_owner='',lease_until=NULL WHERE id=$1",
		f.run.ID,
	)
	require.NoError(t, err)
	var calls int
	var mu sync.Mutex
	var outgoing []telemetry.TraceContext
	registry, moduleContexts := queueTraceModule(t)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/responses/input_tokens" {
			_, _ = w.Write([]byte(`{"input_tokens":10}`))
			return
		}
		mu.Lock()
		calls++
		call := calls
		outgoing = append(
			outgoing,
			telemetry.TraceContext{Parent: r.Header.Get("Traceparent"), State: r.Header.Get("Tracestate")},
		)
		mu.Unlock()
		if call == 1 {
			_, _ = w.Write(
				[]byte(
					`{"id":"read","status":"completed","output":[{"type":"function_call","call_id":"trace-read","name":"script","arguments":"{\"code\":\"return modules.massage.list_slots({specialist_id:'qa'});\"}"}],"usage":{"input_tokens":10,"output_tokens":2}}`,
				),
			)
			return
		}
		if call == 2 {
			_, _ = w.Write(
				[]byte(
					`{"id":"first","status":"completed","output":[{"type":"function_call","call_id":"trace-question","name":"question","arguments":"{\"text\":\"Continue?\",\"choices\":[{\"id\":\"yes\",\"label\":\"Continue\"}],\"allow_text\":false}"}],"usage":{"input_tokens":10,"output_tokens":2}}`,
				),
			)
			return
		}
		_, _ = w.Write(
			[]byte(
				`{"id":"second","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Second result"}]}],"usage":{"input_tokens":10,"output_tokens":2}}`,
			),
		)
	}))
	t.Cleanup(model.Close)
	client, err := provider.NewResponses(model.URL, "test-only", &http.Client{Transport: telemetry.TraceTransport{}})
	require.NoError(t, err)
	start := func() {
		t.Helper()
		engine := runs.New(
			repository,
			client,
			runs.Options{
				TraceStep: executionTraceStep(repository, o),
				Authorize: func(context.Context, domain.Run) (domain.Principal, string, error) { return f.principal, "", nil },
				Script: func(ctx context.Context, run domain.Run, _ domain.Principal, _, _ string) (json.RawMessage, error) {
					p := modules.Principal{
						Issuer:       f.principal.Issuer,
						Subject:      f.principal.Subject,
						Organization: f.principal.OrgID,
						Permissions:  []string{"booking.massage.access", "booking.massage.read"},
					}
					return registry.Broker(p, run.ID, nil).
						Call(ctx, "massage", "list_slots", json.RawMessage(`{"specialist_id":"qa"}`))
				},
			},
		)
		queue, _, err = runtimeQueue(config.Config{}, pool, engine, repository, o, slog.New(slog.DiscardHandler))
		require.NoError(t, err)
		require.NoError(t, queue.Start(t.Context()))
	}
	// Insert before starting consumers, as in a stopped process accepting durable work.
	queue, _, err = runtimeQueue(config.Config{}, pool, nil, repository, o, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	tx, err := pool.Begin(t.Context())
	require.NoError(t, err)
	require.NoError(t, insertRunJob(t.Context(), tx, queue, f.run.ID))
	require.NoError(t, tx.Commit(t.Context()))
	start()
	require.Eventually(t, func() bool {
		r, e := repository.ExecutionRun(t.Context(), f.run.ID)
		return e == nil && r.State == domain.WaitingInput
	}, 10*time.Second, 20*time.Millisecond)
	require.NoError(t, queue.Stop(t.Context()))
	require.Eventually(t, func() bool { return len(c.executions()) == 1 }, 3*time.Second, 20*time.Millisecond)
	first := c.executions()[0]
	origin, previous, _, err := repository.RunTrace(t.Context(), f.run.ID)
	require.NoError(t, err)
	require.Equal(t, origin.SpanContext().TraceID().String(), previous.SpanContext().TraceID().String())
	require.Equal(t, previous.SpanContext().SpanID().String(), hex.EncodeToString(first.GetSpanId()))
	steeringMux := http.NewServeMux()
	var inputErr error
	var acceptedInput telemetry.TraceContext
	steeringMux.HandleFunc("POST /input", func(_ http.ResponseWriter, r *http.Request) {
		acceptedInput = telemetry.CaptureTrace(r.Context())
		_, inputErr = repository.AddInput(
			r.Context(),
			f.principal,
			f.run.ID,
			"steer",
			domain.RunInput{Kind: domain.InputSteering, Text: "continue"},
		)
		if inputErr != nil {
			return
		}
		_, inputErr = repository.AddInput(
			r.Context(),
			f.principal,
			f.run.ID,
			"answer",
			domain.RunInput{Kind: "answer", QuestionID: "trace-question", ChoiceIDs: []string{"yes"}},
		)
	})
	steering := httptest.NewRequest(http.MethodPost, "/input", nil)
	steering.Header.Set("Traceparent", "00-abcdef0123456789abcdef0123456789-abcdef0123456789-01")
	o.HTTP(steeringMux).ServeHTTP(httptest.NewRecorder(), steering)
	require.NoError(t, inputErr)
	_, _, input, err := repository.RunTrace(t.Context(), f.run.ID)
	require.NoError(t, err)
	require.Empty(t, input)
	start()
	t.Cleanup(func() { _ = queue.StopAndCancel(context.Background()) })
	require.Eventually(t, func() bool {
		r, e := repository.ExecutionRun(t.Context(), f.run.ID)
		return e == nil && r.State == domain.Completed
	}, 10*time.Second, 20*time.Millisecond)
	require.NoError(t, queue.Stop(t.Context()))
	require.Eventually(t, func() bool { return len(c.executions()) == 2 }, 3*time.Second, 20*time.Millisecond)
	second := c.executions()[1]
	require.Equal(t, first.GetTraceId(), second.GetTraceId())
	require.Equal(t, origin.SpanContext().SpanID().String(), hex.EncodeToString(second.GetParentSpanId()))
	require.NotEqual(t, first.GetSpanId(), second.GetSpanId())
	require.Len(t, second.GetLinks(), 1)
	require.Equal(t, first.GetSpanId(), second.GetLinks()[0].GetSpanId())
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, outgoing, 3)
	require.Equal(t, hex.EncodeToString(first.GetTraceId()), outgoing[0].SpanContext().TraceID().String())
	require.Equal(t, hex.EncodeToString(first.GetTraceId()), outgoing[1].SpanContext().TraceID().String())
	require.Equal(t, acceptedInput.SpanContext().TraceID(), outgoing[2].SpanContext().TraceID())
	_, _, applied, loadErr := repository.RunTrace(t.Context(), f.run.ID)
	require.NoError(t, loadErr)
	require.Equal(t, acceptedInput, applied)
	select {
	case module := <-moduleContexts:
		require.Equal(t, hex.EncodeToString(first.GetTraceId()), module.SpanContext().TraceID().String())
		require.Equal(t, "qa=origin", module.State)
	default:
		t.Fatal("module HTTP did not receive execution context")
	}
}

func TestQueuedTraceLegacyAndInvalidContextStillExecutes(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`{}`, `null`, `{"traceparent":42}`, `{"traceparent":"invalid"}`} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			f := newCallerRecoveryFixture(t)
			_, err := f.repository.Pool().
				Exec(t.Context(), "UPDATE runs SET state='queued',lease_owner='',lease_until=NULL,trace_origin=$2,trace_attempt=$2,trace_applied=$2,trace_step=$2 WHERE id=$1", f.run.ID, raw)
			require.NoError(t, err)
			c := newQueueTraceReceiver(t)
			o := c.observer(t)
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/responses/input_tokens" {
					_, _ = w.Write([]byte(`{"input_tokens":10}`))
					return
				}
				_, _ = w.Write(
					[]byte(
						`{"id":"fallback","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Completed"}]}],"usage":{"input_tokens":10,"output_tokens":2}}`,
					),
				)
			}))
			t.Cleanup(model.Close)
			client, err := provider.NewResponses(model.URL, "test-only", http.DefaultClient)
			require.NoError(t, err)
			engine := runs.New(
				f.repository,
				client,
				runs.Options{
					TraceStep: executionTraceStep(f.repository, o),
					Authorize: func(context.Context, domain.Run) (domain.Principal, string, error) { return f.principal, "", nil },
				},
			)
			worker := runWorker{engine: engine, repository: f.repository, observer: o}
			require.NoError(t, worker.Work(t.Context(), &river.Job[executeRun]{Args: executeRun{RunID: f.run.ID}}))
			run, err := f.repository.ExecutionRun(t.Context(), f.run.ID)
			require.NoError(t, err)
			require.Equal(t, domain.Completed, run.State)
			require.Eventually(t, func() bool { return len(c.executions()) == 1 }, 3*time.Second, 20*time.Millisecond)
			require.Empty(t, c.executions()[0].GetParentSpanId())
			require.Empty(t, c.executions()[0].GetLinks())
		})
	}
}
