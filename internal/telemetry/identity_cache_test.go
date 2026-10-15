package telemetry_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/telemetry"
)

func TestIdentityCacheMetricsHaveBoundedLabelsAndClaimAge(t *testing.T) {
	t.Parallel()
	observer := telemetry.New(slog.New(slog.DiscardHandler))
	observer.IdentityCache(context.Background(), "hit", 2*time.Second)
	observer.IdentityCache(context.Background(), "miss", 0)
	observer.IdentityCache(context.Background(), "secret-user-token", 0)
	w := httptest.NewRecorder()
	observer.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Contains(t, w.Body.String(), `clx_identity_cache_total{result="hit"} 1`)
	require.Contains(t, w.Body.String(), `clx_identity_cache_age_seconds_sum{cache="permissions"} 2`)
	require.NotContains(t, w.Body.String(), "secret-user-token")
}
