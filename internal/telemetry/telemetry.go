// Package telemetry exposes a fixed set of bounded Prometheus metrics and safe logs.
package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	errorOutcome           = "error"
	statusClassDivisor     = 100
	authorizationComponent = "authorization"
	cancelledOutcome       = "cancelled"
	successOutcome         = "success"
	modelComponent         = "model"
	runComponent           = "run_execution"
)

type runIDKey struct{}

func WithRun(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, runIDKey{}, id)
}
func RunID(ctx context.Context) string { id, _ := ctx.Value(runIDKey{}).(string); return id }

type requestIDKey struct{}

func RequestID(ctx context.Context) string { id, _ := ctx.Value(requestIDKey{}).(string); return id }

type Snapshot struct {
	Gauges map[string]float64 `json:"Gauges"`
	Runs   map[string]int64   `json:"Runs"`
	Queue  map[string]int64   `json:"Queue"`
}
type sample struct {
	Count   uint64
	Sum     float64
	Buckets [8]uint64
}
type HTTPKey struct{ Route, Method, Status string }
type CallKey struct{ Component, Outcome string }
type Telemetry struct {
	traceExportFailures  uint64
	metricExportFailures uint64
	otel                 *otelState
	recoveryMode         bool
	logPayloads          bool
	mu                   sync.Mutex
	logger               *slog.Logger
	started              time.Time
	http                 map[HTTPKey]sample
	calls                map[CallKey]sample
	inFlight             int64
	snapshot             Snapshot
	sampled              time.Time
	dependencies         map[string]bool
}

func histogramBounds() [8]float64 { return [8]float64{.005, .025, .1, .5, 1, 5, 30, 300} }

func New(logger *slog.Logger) *Telemetry {
	return &Telemetry{
		logger:       logger,
		started:      time.Now(),
		http:         map[HTTPKey]sample{},
		calls:        map[CallKey]sample{},
		dependencies: map[string]bool{},
	}
}

// SetRecoveryMode records the deployment dispatch barrier independently of sampled metrics.
func (t *Telemetry) SetRecoveryMode(enabled bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.recoveryMode = enabled
}
func observe(s sample, elapsed time.Duration) sample {
	seconds := elapsed.Seconds()
	s.Count++
	s.Sum += seconds
	for i, b := range histogramBounds() {
		if seconds <= b {
			s.Buckets[i]++
		}
	}
	return s
}

// Publish replaces the snapshot only after a successful database collection.
func (t *Telemetry) Publish(snapshot Snapshot) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.snapshot = snapshot
	t.sampled = time.Now()
}
func (t *Telemetry) Dependency(ctx context.Context, name string, up bool) {
	switch name {
	case "database", "sandbox", "discovery", "artifact_volume", "file_processor":
	default:
		return
	}
	t.mu.Lock()
	previous, seen := t.dependencies[name]
	t.dependencies[name] = up
	t.mu.Unlock()
	if !seen || previous != up {
		level := slog.LevelInfo
		if !up {
			level = slog.LevelError
		}
		t.logger.Log(ctx, level, "dependency status changed", "component", name, "available", up)
	}
}

// Operation admits only fixed components and outcomes, never module names or IDs.
func (t *Telemetry) Operation(ctx context.Context, component, outcome, runID string, started time.Time) {
	if !knownComponent(component) {
		return
	}
	switch outcome {
	case successOutcome, errorOutcome, cancelledOutcome, "timeout", "unknown", "denied":
	default:
		outcome = errorOutcome
	}
	t.otelOperation(ctx, component, outcome, started)
	elapsed := time.Since(started)
	key := CallKey{component, outcome}
	t.mu.Lock()
	t.calls[key] = observe(t.calls[key], elapsed)
	t.mu.Unlock()
	attrs := []any{"component", component, "outcome", outcome, "duration_ms", elapsed.Milliseconds()}
	if _, err := uuid.Parse(runID); err == nil {
		attrs = append(attrs, "run_id", runID)
	}
	level := slog.LevelWarn
	message := "operation stopped"
	if outcome == successOutcome {
		level = slog.LevelDebug
		message = "operation finished"
	}
	if outcome == cancelledOutcome || outcome == "denied" {
		level = slog.LevelDebug
	}
	if outcome == errorOutcome &&
		(component == modelComponent || component == authorizationComponent || component == runComponent) {
		level = slog.LevelError
	}
	t.logger.Log(ctx, level, message, attrs...)
}
func (t *Telemetry) Event(ctx context.Context, event string, attrs ...any) {
	t.logger.InfoContext(ctx, event, attrs...)
}
func (t *Telemetry) HTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r, span := t.otelHTTP(r)
		started := time.Now()
		requestID := uuid.NewString()
		ctx := context.WithValue(r.Context(), requestIDKey{}, requestID)
		r = r.WithContext(ctx)
		w.Header().Set("X-Request-ID", requestID)
		wrapped := &response{ResponseWriter: w, status: http.StatusOK}
		t.mu.Lock()
		t.inFlight++
		t.mu.Unlock()
		defer func() {
			// Patterns come only from registered ServeMux routes; unmatched paths are omitted.
			route := r.Pattern
			if route == "" {
				route = "unmatched"
			}
			method := r.Method
			switch method {
			case "GET", "POST", "DELETE", "PATCH", "PUT", "HEAD", "OPTIONS":
			default:
				method = "OTHER"
			}
			finishHTTP(span, route, method, wrapped.status)
			status := fmt.Sprintf("%dxx", wrapped.status/statusClassDivisor)
			elapsed := time.Since(started)
			key := HTTPKey{route, method, status}
			t.mu.Lock()
			t.inFlight--
			t.http[key] = observe(t.http[key], elapsed)
			t.mu.Unlock()
			level := slog.LevelInfo
			if wrapped.status >= http.StatusInternalServerError {
				level = slog.LevelError
			} else if wrapped.status >= http.StatusBadRequest {
				level = slog.LevelWarn
			}
			if wrapped.status < http.StatusBadRequest && (route == "GET /healthz" || route == "GET /readyz") {
				level = slog.LevelDebug
			}
			t.logger.Log(
				ctx,
				level,
				"http request finished",
				"request_id",
				requestID,
				"route",
				route,
				"method",
				method,
				"status",
				wrapped.status,
				"duration_ms",
				elapsed.Milliseconds(),
			)
		}()
		next.ServeHTTP(wrapped, r)
	})
}

type response struct {
	http.ResponseWriter

	status int
	wrote  bool
}

func (w *response) WriteHeader(status int) {
	if status >= http.StatusContinue && status < http.StatusOK && status != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if !w.wrote {
		w.status = status
		w.wrote = true
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *response) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}
func (w *response) Flush() {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *response) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// ServeHTTP exports cached aggregates. Scrapes never execute scripts or query SQL.
func (t *Telemetry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.Path != "/metrics" {
		http.NotFound(w, r)
		return
	}
	t.mu.Lock()
	var b strings.Builder
	gauge := func(name, help string, value float64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s %g\n", name, help, name, name, value)
	}
	t.processMetrics(gauge, &b)
	gauge("clx_http_in_flight", "HTTP requests currently served.", float64(t.inFlight))
	timestamp := float64(0)
	if !t.sampled.IsZero() {
		timestamp = float64(t.sampled.Unix())
	}
	gauge(
		"clx_snapshot_timestamp_seconds",
		"Last successful database metrics collection; zero before first success.",
		timestamp,
	)
	fmt.Fprintln(&b, "# HELP clx_dependency_up Current dependency availability.\n# TYPE clx_dependency_up gauge")
	for _, name := range []string{"database", "sandbox", "discovery", "artifact_volume", "file_processor"} {
		value := 0
		if t.dependencies[name] {
			value = 1
		}
		fmt.Fprintf(&b, "clx_dependency_up{component=%q} %d\n", name, value)
	}
	for _, name := range sortedKeys(t.snapshot.Gauges) {
		gauge(name, "Current database or filesystem aggregate.", t.snapshot.Gauges[name])
	}
	fmt.Fprintln(&b, "# HELP clx_runs Current durable runs by state.\n# TYPE clx_runs gauge")
	for _, state := range []string{"queued", "running", "waiting_input", "waiting_approval", "waiting_auth", "reconciling", "paused", "completed", cancelledOutcome, "failed"} {
		fmt.Fprintf(&b, "clx_runs{state=%q} %d\n", state, t.snapshot.Runs[state])
	}
	fmt.Fprintln(&b, "# HELP clx_queue_jobs Current River jobs by state.\n# TYPE clx_queue_jobs gauge")
	for _, state := range []string{"available", "scheduled", "running", "retryable", "pending", "completed", cancelledOutcome, "discarded"} {
		fmt.Fprintf(&b, "clx_queue_jobs{state=%q} %d\n", state, t.snapshot.Queue[state])
	}
	fmt.Fprintln(
		&b,
		"# HELP clx_http_requests_total Finished HTTP requests.\n# TYPE clx_http_requests_total counter\n# HELP clx_http_request_duration_seconds HTTP request duration including SSE connection lifetime.\n# TYPE clx_http_request_duration_seconds histogram",
	)
	// Sorting keeps exposition stable and each metric family contiguous.
	httpKeys := make([]HTTPKey, 0, len(t.http))
	for key := range t.http {
		httpKeys = append(httpKeys, key)
	}
	sort.Slice(httpKeys, func(i, j int) bool { return fmt.Sprint(httpKeys[i]) < fmt.Sprint(httpKeys[j]) })
	for _, key := range httpKeys {
		fmt.Fprintf(
			&b,
			"clx_http_requests_total{route=%q,method=%q,status=%q} %d\n",
			key.Route,
			key.Method,
			key.Status,
			t.http[key].Count,
		)
	}
	for _, key := range httpKeys {
		labels := fmt.Sprintf("route=%q,method=%q,status=%q", key.Route, key.Method, key.Status)
		histogram(&b, "clx_http_request_duration_seconds", labels, t.http[key])
	}
	fmt.Fprintln(
		&b,
		"# HELP clx_operations_total Finished dependency and execution operations.\n# TYPE clx_operations_total counter\n# HELP clx_operation_duration_seconds Dependency and execution operation duration.\n# TYPE clx_operation_duration_seconds histogram",
	)
	keys := make([]CallKey, 0, len(t.calls))
	for key := range t.calls {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j]) })
	for _, key := range keys {
		fmt.Fprintf(
			&b,
			"clx_operations_total{component=%q,outcome=%q} %d\n",
			key.Component,
			key.Outcome,
			t.calls[key].Count,
		)
	}
	for _, key := range keys {
		histogram(
			&b,
			"clx_operation_duration_seconds",
			fmt.Sprintf("component=%q,outcome=%q", key.Component, key.Outcome),
			t.calls[key],
		)
	}
	t.mu.Unlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = fmt.Fprint(w, b.String())
}
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
func histogram(b *strings.Builder, name, labels string, s sample) {
	for i, bound := range histogramBounds() {
		fmt.Fprintf(b, "%s_bucket{%s,le=%q} %d\n", name, labels, fmt.Sprint(bound), s.Buckets[i])
	}
	fmt.Fprintf(
		b,
		"%s_bucket{%s,le=\"+Inf\"} %d\n%s_sum{%s} %g\n%s_count{%s} %d\n",
		name,
		labels,
		s.Count,
		name,
		labels,
		s.Sum,
		name,
		labels,
		s.Count,
	)
}

// recoveryGauge is called while the telemetry lock is held.
func (t *Telemetry) recoveryGauge() float64 {
	if t.recoveryMode {
		return 1
	}
	return 0
}

func (t *Telemetry) processMetrics(gauge func(string, string, float64), b *strings.Builder) {
	t.exportFailureMetrics(b)
	gauge("clx_recovery_mode", "Restore dispatch barrier enabled.", t.recoveryGauge())
	gauge("clx_process_start_time_seconds", "Unix process start time.", float64(t.started.Unix()))
}

// exportFailureMetrics is called while the telemetry lock is held.
func (t *Telemetry) exportFailureMetrics(b *strings.Builder) {
	fmt.Fprintln(
		b,
		"# HELP clx_otel_export_failures_total OTLP export requests dropped after failure, including partial rejection.\n# TYPE clx_otel_export_failures_total counter",
	)
	fmt.Fprintf(
		b,
		"clx_otel_export_failures_total{signal=%q} %d\nclx_otel_export_failures_total{signal=%q} %d\n",
		"traces",
		t.traceExportFailures,
		"metrics",
		t.metricExportFailures,
	)
}
