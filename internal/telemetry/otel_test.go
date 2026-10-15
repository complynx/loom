package telemetry_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	collectormetric "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/telemetry"
)

func TestOTelExportPrivacyAndCorrelation(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var receipts [][]byte
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" && r.URL.Path != "/v1/metrics" {
			http.NotFound(w, r)
			return
		}
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		receipts = append(receipts, b)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	var logs bytes.Buffer
	logger := telemetry.CorrelateLogger(slog.New(slog.NewJSONHandler(&logs, nil)))
	observer := telemetry.New(logger)
	cfg := config.OTel{Enabled: true, Endpoint: collector.URL, SampleRatio: 1}
	cfg.ApplyDefaults()
	shutdown, err := observer.EnableOTel(t.Context(), cfg)
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /items/{id}", func(w http.ResponseWriter, r *http.Request) {
		ctx, done := observer.Start(r.Context(), "module_rpc", "")
		logger.InfoContext(ctx, "nested operation")
		observer.Operation(ctx, "module_rpc", "success", "", time.Now())
		done(nil)
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/items/PRIVATE_ITEM?secret=PRIVATE_QUERY", nil)
	req.Header.Set("Authorization", "Bearer PRIVATE_TOKEN")
	req.Header.Set("Baggage", "private=PRIVATE_BAGGAGE")
	req.Header.Set("Traceparent", "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
	observer.HTTP(mux).ServeHTTP(httptest.NewRecorder(), req)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	require.NoError(t, shutdown(ctx))
	require.Contains(t, logs.String(), `"trace_id":"0123456789abcdef0123456789abcdef"`)
	mu.Lock()
	defer mu.Unlock()
	require.GreaterOrEqual(t, len(receipts), 2)
	joined := string(bytes.Join(receipts, nil))
	require.Contains(t, joined, "GET /items/{id}")
	require.Contains(t, joined, "loom.otel.operations")
	for _, secret := range []string{"PRIVATE_ITEM", "PRIVATE_QUERY", "PRIVATE_TOKEN", "PRIVATE_BAGGAGE"} {
		require.NotContains(t, joined, secret)
	}
}
func TestOTelOutageDoesNotBlockRequestOrShutdown(t *testing.T) {
	t.Parallel()
	collector := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }),
	)
	defer collector.Close()
	observer := telemetry.New(slog.New(slog.DiscardHandler))
	cfg := config.OTel{Enabled: true, Endpoint: collector.URL, SampleRatio: 1, TimeoutMS: 20, MaxQueue: 2, MaxBatch: 1}
	cfg.ApplyDefaults()
	shutdown, err := observer.EnableOTel(t.Context(), cfg)
	require.NoError(t, err)
	for range 200 {
		ctx, done := observer.Start(t.Context(), "script", "")
		observer.Operation(ctx, "script", "success", "", time.Now())
		done(nil)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_ = shutdown(ctx)
	require.NoError(t, ctx.Err())
}
func TestTraceTransportPropagatesWithoutMutatingHeaders(t *testing.T) {
	t.Parallel()
	headers := make(chan http.Header, 1)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			headers <- r.Header.Clone()
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer collector.Close()
	observer := telemetry.New(slog.New(slog.DiscardHandler))
	cfg := config.OTel{Enabled: true, Endpoint: collector.URL, SampleRatio: 1}
	cfg.ApplyDefaults()
	shutdown, err := observer.EnableOTel(t.Context(), cfg)
	require.NoError(t, err)
	ctx, done := observer.Start(t.Context(), "model", "")
	defer done(nil)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, collector.URL, nil)
	require.NoError(t, err)
	request.Header.Set("Baggage", "private=must-not-leave")
	response, err := (telemetry.TraceTransport{}).RoundTrip(request)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	header := <-headers
	require.True(t, strings.HasPrefix(header.Get("Traceparent"), "00-"))
	require.Empty(t, header.Get("Baggage"))
	require.Empty(t, request.Header.Get("Traceparent"))
	require.Equal(t, "private=must-not-leave", request.Header.Get("Baggage"))
	done(nil)
	timeout, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_ = shutdown(timeout)
}
func TestOTelShutdownHonorsCallerDeadline(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	collector := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	}))
	defer collector.Close()
	defer close(release)
	observer := telemetry.New(slog.New(slog.DiscardHandler))
	cfg := config.OTel{
		Enabled:     true,
		Endpoint:    collector.URL,
		SampleRatio: 1,
		TimeoutMS:   1000,
		MaxQueue:    2,
		MaxBatch:    1,
	}
	cfg.ApplyDefaults()
	shutdown, err := observer.EnableOTel(t.Context(), cfg)
	require.NoError(t, err)
	_, done := observer.Start(t.Context(), "script", "")
	done(nil)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("trace exporter did not reach collector")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result := make(chan error, 1)
	go func() { result <- shutdown(ctx) }()
	select {
	case shutdownErr := <-result:
		require.ErrorIs(t, shutdownErr, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("shutdown ignored caller cancellation")
	}
}
func TestCollectorFailuresAreReportedWithoutRemoteMessages(t *testing.T) {
	t.Parallel()
	for _, partial := range []bool{false, true} {
		t.Run(strconv.FormatBool(partial), func(t *testing.T) {
			t.Parallel()
			const secret = "REMOTE_SECRET_MUST_NOT_BE_LOGGED"
			var logs bytes.Buffer
			collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/traces" && r.URL.Path != "/v1/metrics" {
					http.NotFound(w, r)
					return
				}
				if !partial {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = io.WriteString(w, secret)
					return
				}
				w.Header().Set("Content-Type", "application/x-protobuf")
				var message proto.Message
				if r.URL.Path == "/v1/traces" {
					message = &collectortrace.ExportTraceServiceResponse{
						PartialSuccess: &collectortrace.ExportTracePartialSuccess{
							RejectedSpans: 1,
							ErrorMessage:  secret,
						},
					}
				} else {
					message = &collectormetric.ExportMetricsServiceResponse{
						PartialSuccess: &collectormetric.ExportMetricsPartialSuccess{
							RejectedDataPoints: 1,
							ErrorMessage:       secret,
						},
					}
				}
				body, marshalErr := proto.Marshal(message)
				if marshalErr != nil {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				_, _ = w.Write(body)
			}))
			defer collector.Close()
			observer := telemetry.New(slog.New(slog.NewJSONHandler(&logs, nil)))
			cfg := config.OTel{Enabled: true, Endpoint: collector.URL, SampleRatio: 1, MaxQueue: 2, MaxBatch: 1}
			cfg.ApplyDefaults()
			shutdown, err := observer.EnableOTel(t.Context(), cfg)
			require.NoError(t, err)
			ctx, done := observer.Start(t.Context(), "model", "")
			observer.Operation(ctx, "model", "success", "", time.Now())
			done(nil)
			timeout, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			require.NoError(t, shutdown(timeout))
			require.NotContains(t, logs.String(), secret)
			require.Contains(t, logs.String(), `"msg":"otel export dropped"`)
			require.Contains(t, logs.String(), `"signal":"traces"`)
			require.Contains(t, logs.String(), `"signal":"metrics"`)
			scrape := httptest.NewRecorder()
			observer.ServeHTTP(scrape, httptest.NewRequest(http.MethodGet, "/metrics", nil))
			require.Contains(t, scrape.Body.String(), `clx_otel_export_failures_total{signal="traces"} 1`)
			require.Contains(t, scrape.Body.String(), `clx_otel_export_failures_total{signal="metrics"} 1`)
		})
	}
}
