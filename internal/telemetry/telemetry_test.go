package telemetry_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/telemetry"
)

func TestMetricsAndLogsExcludeClientDataAndPreserveStreaming(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	observer := telemetry.New(slog.New(slog.NewJSONHandler(&logs, nil)))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/runs/{id}/events", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		_, err := io.WriteString(w, "data: private-output\n\n")
		if err != nil {
			t.Errorf("stream write: %v", err)
		}
	})
	request := httptest.NewRequest(http.MethodGet, "/v1/runs/private-user/events?token=secret-token", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	request.Header.Set("X-Request-ID", "private-input")
	response := httptest.NewRecorder()
	observer.HTTP(mux).ServeHTTP(response, request)
	require.True(t, response.Flushed)
	require.Contains(t, response.Body.String(), "private-output")
	require.NotEqual(t, "private-input", response.Header().Get("X-Request-ID"))
	for range 50 {
		observer.HTTP(mux).
			ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/unknown/private-value", nil))
	}
	observer.Operation(context.Background(), "model", "unknown", "not-a-uuid-private-value", time.Now())
	observer.Dependency(context.Background(), "database", true)
	observer.Publish(
		telemetry.Snapshot{
			Runs:   map[string]int64{"reconciling": 2},
			Gauges: map[string]float64{"clx_reserved_microcredits": 500},
		},
	)
	metrics := httptest.NewRecorder()
	observer.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, metrics.Code)
	require.Contains(t, metrics.Body.String(), `clx_runs{state="reconciling"} 2`)
	require.Contains(t, metrics.Body.String(), `clx_operations_total{component="model",outcome="unknown"} 1`)
	require.Contains(t, metrics.Body.String(), `route="GET /v1/runs/{id}/events"`)
	for _, secret := range []string{"secret-token", "private-user", "private-value", "private-output", "private-input"} {
		require.NotContains(t, logs.String(), secret)
		require.NotContains(t, metrics.Body.String(), secret)
	}
	require.NotContains(t, metrics.Body.String(), "run_id=")
	observer.Dependency(context.Background(), "database", false)
	metrics = httptest.NewRecorder()
	observer.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Contains(t, metrics.Body.String(), `clx_dependency_up{component="database"} 0`)
	require.Contains(t, metrics.Body.String(), "clx_reserved_microcredits 500")
}
func TestSlowScrapeDoesNotBlockOperations(t *testing.T) {
	t.Parallel()
	observer := telemetry.New(slog.New(slog.DiscardHandler))
	writer := &blockedWriter{
		ResponseWriter: httptest.NewRecorder(),
		entered:        make(chan struct{}),
		release:        make(chan struct{}),
	}
	done := make(chan struct{})
	go func() { observer.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/metrics", nil)); close(done) }()
	<-writer.entered
	updated := make(chan struct{})
	go func() { observer.Operation(context.Background(), "script", "timeout", "", time.Now()); close(updated) }()
	select {
	case <-updated:
	case <-time.After(time.Second):
		t.Error("slow metrics client blocks execution instrumentation")
	}
	close(writer.release)
	<-done
}

type blockedWriter struct {
	http.ResponseWriter

	entered, release chan struct{}
}

func (w *blockedWriter) Write(p []byte) (int, error) {
	close(w.entered)
	<-w.release
	return w.ResponseWriter.Write(p)
}
func TestZeroSnapshotAndUnknownComponents(t *testing.T) {
	t.Parallel()
	observer := telemetry.New(slog.New(slog.DiscardHandler))
	observer.Operation(context.Background(), "user-controlled", "private-value", "", time.Now())
	response := httptest.NewRecorder()
	observer.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Contains(t, response.Body.String(), "clx_snapshot_timestamp_seconds 0\n")
	require.NotContains(t, response.Body.String(), "private-value")
}
