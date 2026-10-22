package telemetry_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"github.com/complynx/loom/internal/telemetry"
)

func TestDurableTraceValidation(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`{}`, `null`, `[]`, `{"traceparent":42}`, `{"traceparent":"invalid"}`,
		`{"traceparent":"00-0123456789abcdef0123456789abcdef-0123456789abcdef-+1"}`,
		`{"traceparent":"00-0123456789abcdef0123456789abcdef-0123456789abcdef-0F"}`,
		`{"traceparent":"00-00000000000000000000000000000000-0123456789abcdef-01"}`} {
		require.False(t, telemetry.DecodeTrace([]byte(raw)).SpanContext().IsValid())
	}
	c := telemetry.DecodeTrace(
		[]byte(
			`{"traceparent":"00-0123456789abcdef0123456789abcdef-0123456789abcdef-00","tracestate":"vendor=value","baggage":"secret"}`,
		),
	)
	require.True(t, c.SpanContext().IsValid())
	require.False(t, c.SpanContext().IsSampled())
	require.Equal(t, "vendor=value", c.SpanContext().TraceState().String())
	require.Equal(
		t,
		c,
		telemetry.CaptureTrace(trace.ContextWithRemoteSpanContext(context.Background(), c.SpanContext())),
	)
	invalidState := c
	invalidState.State = "invalid state"
	raw, err := json.Marshal(invalidState)
	require.NoError(t, err)
	require.Equal(t, c.Parent, telemetry.DecodeTrace(raw).Parent)
	require.Empty(t, telemetry.DecodeTrace(raw).State)
	invalidState.State = strings.Repeat("x", 513)
	raw, err = json.Marshal(invalidState)
	require.NoError(t, err)
	require.Empty(t, telemetry.DecodeTrace(raw))
}
