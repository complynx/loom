package provider_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/provider"
)

func TestShortRetryHTTPClassification(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, code string
		status     int
		calls      int
	}{
		{"rate limit", "rate_limit_exceeded", 429, 2},
		{"quota", "insufficient_quota", 429, 1},
		{"overload", "server_is_overloaded", 503, 2},
		{"unknown upstream", "gateway_unknown", 503, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if calls.Add(1) == 1 {
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(`{"error":{"code":"` + tc.code + `","message":"secret prompt"}}`))
					return
				}
				_, _ = w.Write(
					[]byte(
						`{"id":"known","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`,
					),
				)
			}))
			defer server.Close()
			client, err := provider.NewResponses(server.URL, "secret", server.Client())
			require.NoError(t, err)
			response, err := provider.ShortRetry(
				context.Background(),
				2,
				3*time.Second,
				func(ctx context.Context) (provider.Response, error) {
					return client.Respond(
						ctx,
						provider.Request{
							Model:           "test",
							MaxOutputTokens: 1,
							Input:           []provider.Item{provider.Message("user", "hello")},
						},
					)
				},
			)
			require.Equal(t, tc.calls, int(calls.Load()))
			if tc.calls == 2 {
				require.NoError(t, err)
				require.Equal(t, "known", response.ID)
			} else {
				require.Error(t, err)
				require.NotContains(t, err.Error(), "secret")
			}
		})
	}
}

func TestRetryAfterAndCancellationDoNotWaitOrDispatchAgain(t *testing.T) {
	t.Parallel()
	for _, header := range []string{"30", time.Now().Add(time.Minute).UTC().Format(http.TimeFormat)} {
		t.Run(header, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", header)
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"code":"rate_limit_exceeded"}}`))
			}))
			defer server.Close()
			client, err := provider.NewResponses(server.URL, "secret", server.Client())
			require.NoError(t, err)
			started := time.Now()
			_, err = provider.ShortRetry(
				context.Background(),
				2,
				3*time.Second,
				func(ctx context.Context) (provider.Response, error) {
					return client.Respond(
						ctx,
						provider.Request{
							Model:           "test",
							MaxOutputTokens: 1,
							Input:           []provider.Item{provider.Message("user", "hello")},
						},
					)
				},
			)
			require.Error(t, err)
			require.EqualValues(t, 1, calls.Load())
			require.Less(t, time.Since(started), time.Second)
			var failure *provider.Failure
			require.ErrorAs(t, err, &failure)
			require.Greater(t, failure.RetryAfter, 20*time.Second)
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, err := provider.ShortRetry(ctx, 2, 3*time.Second, func(context.Context) (provider.Response, error) {
		calls++
		cancel()
		return provider.Response{}, &provider.Failure{Code: "rate_limit_exceeded", Status: 429}
	})
	require.Error(t, err)
	require.Equal(t, 1, calls)
}
