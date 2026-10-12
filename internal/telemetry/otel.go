package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/google/uuid"

	"github.com/complynx/loom/internal/config"
)

const exporterCount = 2

type operationSpanKey struct{}

type otelState struct {
	tracer     trace.Tracer
	operations metric.Int64Counter
	duration   metric.Float64Histogram
}

// EnableOTel installs scoped providers. Exporter failures never block request work.
// Call shutdown with the process shutdown deadline; no global provider is modified.
func (t *Telemetry) EnableOTel(ctx context.Context, c config.OTel) (func(context.Context) error, error) {
	if !c.Enabled {
		return func(context.Context) error { return nil }, nil
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	timeout := time.Duration(c.TimeoutMS) * time.Millisecond
	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("otel requires the standard HTTP transport")
	}
	transport := baseTransport.Clone()
	transport.MaxConnsPerHost = 2
	client := &http.Client{
		Timeout:       timeout,
		Transport:     transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	tx, err := otlptracehttp.New(
		ctx,
		otlptracehttp.WithEndpointURL(c.Endpoint),
		otlptracehttp.WithURLPath("/v1/traces"),
		otlptracehttp.WithTimeout(timeout),
		otlptracehttp.WithHTTPClient(client),
		otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: false}),
	)
	if err != nil {
		return nil, safeExportError(err)
	}
	mx, err := otlpmetrichttp.New(
		ctx,
		otlpmetrichttp.WithEndpointURL(c.Endpoint),
		otlpmetrichttp.WithURLPath("/v1/metrics"),
		otlpmetrichttp.WithTimeout(timeout),
		otlpmetrichttp.WithHTTPClient(client),
		otlpmetrichttp.WithRetry(otlpmetrichttp.RetryConfig{Enabled: false}),
	)
	if err != nil {
		_ = tx.Shutdown(ctx)
		return nil, safeExportError(err)
	}
	res := resource.NewSchemaless(attribute.String("service.name", c.ServiceName))
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(c.SampleRatio))),
		sdktrace.WithBatcher(
			safeTraceExporter{SpanExporter: tx, observer: t},
			sdktrace.WithMaxQueueSize(c.MaxQueue),
			sdktrace.WithMaxExportBatchSize(c.MaxBatch),
			sdktrace.WithBatchTimeout(timeout),
			sdktrace.WithExportTimeout(timeout),
		),
	)
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(
			sdkmetric.NewPeriodicReader(
				safeMetricExporter{Exporter: mx, observer: t},
				sdkmetric.WithInterval(time.Duration(c.ExportIntervalMS)*time.Millisecond),
				sdkmetric.WithTimeout(timeout),
			),
		),
	)
	meter := mp.Meter("github.com/complynx/loom")
	count, _ := meter.Int64Counter("loom.otel.operations")
	bounds := histogramBounds()
	duration, _ := meter.Float64Histogram(
		"loom.otel.operation.duration",
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(bounds[:]...),
	)
	t.otel = &otelState{tracer: tp.Tracer("github.com/complynx/loom"), operations: count, duration: duration}

	return func(ctx context.Context) error {
		defer transport.CloseIdleConnections()
		// Providers flush concurrently within the same caller deadline.
		results := make(chan error, exporterCount)
		go func() { results <- tp.Shutdown(ctx) }()
		go func() { results <- mp.Shutdown(ctx) }()
		var failures []error
		for range exporterCount {
			select {
			case shutdownErr := <-results:
				failures = append(failures, safeExportError(shutdownErr))
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return errors.Join(failures...)
	}, nil
}

// Start creates a nested operation span. Errors are represented by status only;
// exception text may contain credentials or file contents and is never exported.
//
//nolint:spancheck // The returned finish callback owns span.End.
func (t *Telemetry) Start(ctx context.Context, component, runID string) (context.Context, func(error)) {
	if !knownComponent(component) || t.otel == nil {
		return ctx, func(error) {}
	}
	if _, parseErr := uuid.Parse(runID); parseErr == nil {
		ctx = WithRun(ctx, runID)
	}
	ctx, span := t.otel.tracer.Start(ctx, component)
	ctx = context.WithValue(ctx, operationSpanKey{}, component)
	return ctx, func(err error) {
		if err != nil {
			span.SetStatus(codes.Error, "operation failed")
		}
		span.End()
	}
}
func knownComponent(component string) bool {
	switch component {
	case "diagnostics",
		modelComponent,
		"file_processing",
		"transcription",
		"token_count",
		"authorization",
		"module_rpc",
		"module_call",
		"script",
		"artifact_gc",
		"queue_recovery",
		"lifecycle",
		runComponent:
		return true
	default:
		return false
	}
}
func (t *Telemetry) otelOperation(ctx context.Context, component, outcome string, started time.Time) {
	if t.otel == nil {
		return
	}
	attrs := []attribute.KeyValue{attribute.String("component", component), attribute.String("outcome", outcome)}
	t.otel.operations.Add(ctx, 1, metric.WithAttributes(attrs...))
	t.otel.duration.Record(ctx, time.Since(started).Seconds(), metric.WithAttributes(attrs...))
	if existing, _ := ctx.Value(operationSpanKey{}).(string); existing == component {
		return
	}
	_, span := t.otel.tracer.Start(ctx, component, trace.WithTimestamp(started), trace.WithAttributes(attrs...))
	if outcome != successOutcome {
		span.SetStatus(codes.Error, "operation stopped")
	}
	span.End()
}

//nolint:spancheck // HTTP middleware owns span.End in its deferred completion.
func (t *Telemetry) otelHTTP(r *http.Request) (*http.Request, trace.Span) {
	if t.otel == nil {
		return r, nil
	}
	ctx := propagation.TraceContext{}.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
	ctx, span := t.otel.tracer.Start(ctx, "http.request", trace.WithSpanKind(trace.SpanKindServer))
	return r.WithContext(ctx), span
}
func finishHTTP(span trace.Span, route, method string, status int) {
	if span == nil {
		return
	}
	route = strings.TrimPrefix(route, method+" ")
	span.SetName(method + " " + route)
	span.SetAttributes(
		attribute.String("http.route", route),
		attribute.String("http.request.method", method),
		attribute.Int("http.response.status_code", status),
	)
	if status >= http.StatusInternalServerError {
		span.SetStatus(codes.Error, "HTTP server error")
	}
	span.End()
}

type correlationHandler struct{ slog.Handler }

func (h correlationHandler) Handle(ctx context.Context, r slog.Record) error {
	sc := trace.SpanContextFromContext(ctx)
	if sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.Handler.Handle(ctx, r)
}
func (h correlationHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return correlationHandler{h.Handler.WithAttrs(attrs)}
}
func (h correlationHandler) WithGroup(name string) slog.Handler {
	return correlationHandler{h.Handler.WithGroup(name)}
}

// CorrelateLogger adds trace IDs to records whose context contains a span.
func CorrelateLogger(logger *slog.Logger) *slog.Logger {
	return slog.New(correlationHandler{logger.Handler()})
}

// InjectTrace forwards only W3C trace context. It never forwards baggage.
func InjectTrace(ctx context.Context, headers http.Header) {
	headers.Del("Baggage")
	propagation.TraceContext{}.Inject(ctx, propagation.HeaderCarrier(headers))
}

// TraceTransport propagates the caller span through a cloned request.
// Requests, headers, and transport configuration remain owned by the caller.
type TraceTransport struct{ Base http.RoundTripper }

func (t TraceTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.Header = r.Header.Clone()
	InjectTrace(r.Context(), clone.Header)
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}
