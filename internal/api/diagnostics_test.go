package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/clx-scripter/internal/api"
	"github.com/complynx/clx-scripter/internal/domain"
)

func TestDiagnosticsRightsAndCachedHealth(t *testing.T) {
	t.Parallel()
	handler := api.New(nil, authFunc(allowed), api.Options{})
	for _, path := range []string{"/v1/admin/diagnostics", "/v1/admin/diagnostics/health", "/v1/admin/diagnostics/runs", "/v1/admin/diagnostics/runs/private-run"} {
		w := request(handler, "GET", path, "", "", "")
		require.Equal(t, http.StatusForbidden, w.Code)
	}
	auth := authFunc(func(ctx context.Context, r *http.Request) (domain.Principal, error) {
		p, err := allowed(ctx, r)
		p.Permissions["diagnostics.read"] = true
		return p, err
	})
	handler = api.New(nil, auth, api.Options{DiagnosticsHealth: func() domain.DiagnosticsHealth {
		return domain.DiagnosticsHealth{Dependencies: map[string]bool{"database": false}}
	}})
	w := request(handler, "GET", "/v1/admin/diagnostics/health", "", "", "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"database":false`)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
}

func TestOperatorShellContainsNoRuntimeData(t *testing.T) {
	t.Parallel()
	handler := api.New(nil, nil, api.Options{})
	for _, path := range []string{"/admin", "/admin/operator.js", "/admin/operator.css"} {
		w := request(handler, "GET", path, "", "", "")
		require.Equal(t, http.StatusOK, w.Code)
		require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		require.Contains(t, w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'")
		require.NotContains(t, w.Body.String(), "localStorage")
		require.NotContains(t, w.Body.String(), "sessionStorage")
	}
}
