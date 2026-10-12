package telemetry_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/telemetry"
)

func TestRecoveryGaugeSurvivesCollection(t *testing.T) {
	t.Parallel()
	observer := telemetry.New(nil)
	observer.SetRecoveryMode(true)
	observer.Publish(telemetry.Snapshot{})
	require.True(t, observer.DiagnosticsHealth().RecoveryMode)
	w := httptest.NewRecorder()
	observer.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Contains(t, w.Body.String(), "clx_recovery_mode 1")
}
