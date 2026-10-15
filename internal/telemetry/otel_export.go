package telemetry

import (
	"context"
	"errors"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Export failures are dropped deliberately: returning nil prevents the SDK's
// process-global error handler from printing untrusted collector response bodies.
type safeTraceExporter struct {
	sdktrace.SpanExporter

	observer *Telemetry
}

func (e safeTraceExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	if e.SpanExporter.ExportSpans(ctx, spans) != nil {
		e.observer.exportFailed(ctx, "traces")
	}
	return nil
}
func (e safeTraceExporter) Shutdown(ctx context.Context) error {
	return safeExportError(e.SpanExporter.Shutdown(ctx))
}

type safeMetricExporter struct {
	sdkmetric.Exporter

	observer *Telemetry
}

func (e safeMetricExporter) Export(ctx context.Context, data *metricdata.ResourceMetrics) error {
	if e.Exporter.Export(ctx, data) != nil {
		e.observer.exportFailed(ctx, "metrics")
	}
	return nil
}
func (e safeMetricExporter) ForceFlush(ctx context.Context) error {
	return safeExportError(e.Exporter.ForceFlush(ctx))
}
func (e safeMetricExporter) Shutdown(ctx context.Context) error {
	return safeExportError(e.Exporter.Shutdown(ctx))
}
func safeExportError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	default:
		return errors.New("otel exporter lifecycle failed")
	}
}
func (t *Telemetry) exportFailed(ctx context.Context, signal string) {
	t.mu.Lock()
	if signal == "traces" {
		t.traceExportFailures++
	} else {
		t.metricExportFailures++
	}
	t.mu.Unlock()
	t.logger.WarnContext(ctx, "otel export dropped", "signal", signal, "outcome", errorOutcome)
}
