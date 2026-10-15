package provider

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRetryAfterPreservesLongWaitsAndSaturatesOverflow(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	require.Equal(t, 48*time.Hour, retryAfter("172800", now))
	require.Equal(t, 48*time.Hour, retryAfter(now.Add(48*time.Hour).Format(http.TimeFormat), now))
	for _, value := range []string{"9223372036854775807", "9223372036854775808", "99999999999999999999999999999999999999"} {
		require.Equal(t, time.Duration(1<<63-1), retryAfter(value, now))
	}
	require.Zero(t, retryAfter("-99999999999999999999999999", now))
}

func TestShortRetryOverflowWaitReturnsKnownRejectionImmediately(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	calls := 0
	_, err := ShortRetry(ctx, 2, 3*time.Second, func(context.Context) (Response, error) {
		calls++
		return Response{}, &Failure{
			Code:       "rate_limit_exceeded",
			Status:     http.StatusTooManyRequests,
			RetryAfter: time.Duration(1<<63 - 1),
		}
	})
	require.Error(t, err)
	require.Equal(t, 1, calls)
	require.NoError(t, ctx.Err(), "a wait beyond the budget must return before the context deadline")
	var failure *Failure
	require.ErrorAs(t, err, &failure)
	require.False(t, failure.UnknownUsage)
}
