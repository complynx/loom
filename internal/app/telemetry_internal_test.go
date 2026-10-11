package app

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQueueLogsCannotIncludeArgumentsOrRawErrors(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	logger := slog.New(queueLogHandler{next: slog.NewJSONHandler(&out, nil)}).With("job_args", "private-input")
	logger.ErrorContext(context.Background(), "private-result", "error", "Bearer secret-token", "job_id", 123)
	require.Contains(t, out.String(), "queue event")
	require.Contains(t, out.String(), `"level":"ERROR"`)
	for _, secret := range []string{"private-input", "private-result", "secret-token"} {
		require.NotContains(t, out.String(), secret)
	}
}
