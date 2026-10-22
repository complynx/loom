package telemetry

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// TraceContext is a bounded W3C carrier. It contains no baggage or request data.
type TraceContext struct {
	Parent string `json:"traceparent,omitempty"`
	State  string `json:"tracestate,omitempty"`
}

// MaxTraceInputs bounds one consumed batch below the SDK's default span-link limit.
const MaxTraceInputs = 64

// CaptureTrace copies values only, including the sampling decision.
func CaptureTrace(ctx context.Context) TraceContext {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	value := TraceContext{Parent: carrier.Get("traceparent"), State: carrier.Get("tracestate")}
	if !value.SpanContext().IsValid() {
		return TraceContext{}
	}
	return value
}

// SpanContext rejects oversized/invalid carriers. An invalid tracestate is dropped.
func (c TraceContext) SpanContext() trace.SpanContext {
	const hexDigits = "0123456789abcdef"
	if len(c.Parent) != 55 || len(c.State) > 512 || c.Parent[:3] != "00-" || c.Parent[35] != '-' ||
		c.Parent[52] != '-' || strings.IndexByte(hexDigits, c.Parent[53]) < 0 || strings.IndexByte(hexDigits, c.Parent[54]) < 0 {
		return trace.SpanContext{}
	}
	id, idErr := trace.TraceIDFromHex(c.Parent[3:35])
	span, spanErr := trace.SpanIDFromHex(c.Parent[36:52])
	flags, flagsErr := strconv.ParseUint(c.Parent[53:], 16, 8)
	if idErr != nil || spanErr != nil || flagsErr != nil {
		return trace.SpanContext{}
	}
	state, _ := trace.ParseTraceState(c.State)
	return trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    id,
		SpanID:     span,
		TraceFlags: trace.TraceFlags(flags) & trace.FlagsSampled,
		TraceState: state,
		Remote:     true,
	})
}

// DecodeTrace tolerates legacy, missing and malformed diagnostic data.
func DecodeTrace(raw []byte) TraceContext {
	var c TraceContext
	if len(raw) > 4096 || json.Unmarshal(raw, &c) != nil || !c.SpanContext().IsValid() {
		return TraceContext{}
	}
	return TraceContext{Parent: c.Parent, State: c.SpanContext().TraceState().String()}
}

// StartRun restores the applied parent and links the origin and previous attempt.
// The worker context still owns cancellation; no ingress context is kept alive.
func (t *Telemetry) StartRun(
	ctx context.Context,
	runID string,
	origin, previous, applied TraceContext,
) (context.Context, func(error)) {
	parent := applied.SpanContext()
	if !parent.IsValid() {
		parent = origin.SpanContext()
	}
	ctx = trace.ContextWithRemoteSpanContext(ctx, parent)
	const maxLinks = 2
	links := make([]trace.Link, 0, maxLinks)
	for _, candidate := range []TraceContext{previous, origin} {
		sc := candidate.SpanContext()
		if !sc.IsValid() || sc.Equal(parent) || len(links) > 0 && sc.Equal(links[0].SpanContext) {
			continue
		}
		links = append(links, trace.Link{SpanContext: sc})
	}
	return t.Start(ctx, runComponent, runID, trace.WithLinks(links...))
}

// StartStep uses the applied input as parent and links the actual contributing inputs.
func (t *Telemetry) StartStep(
	ctx context.Context,
	runID string,
	origin, parent, previous TraceContext,
	inputs []TraceContext,
) (context.Context, func(error)) {
	if !parent.SpanContext().IsValid() {
		parent = origin
	}
	ctx = trace.ContextWithRemoteSpanContext(ctx, parent.SpanContext())
	const priorLinks = 2
	links := make([]trace.Link, 0, len(inputs)+priorLinks)
	for _, candidate := range append([]TraceContext{origin, previous}, inputs...) {
		sc := candidate.SpanContext()
		if !sc.IsValid() {
			continue
		}
		duplicate := false
		for _, link := range links {
			if link.SpanContext.TraceID() == sc.TraceID() && link.SpanContext.SpanID() == sc.SpanID() {
				duplicate = true
				break
			}
		}
		if !duplicate {
			links = append(links, trace.Link{SpanContext: sc})
		}
	}
	return t.Start(ctx, "execution_step", runID, trace.WithLinks(links...))
}
