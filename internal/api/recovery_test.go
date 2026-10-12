package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/api"
	"github.com/complynx/loom/internal/domain"
)

func TestRecoveryBlocksExecutionAndImplicitWrites(t *testing.T) {
	t.Parallel()
	handler := api.New(
		nil,
		authFunc(allowed),
		api.Options{
			RecoveryMode: true,
			Ready:        func(context.Context) error { t.Error("recovery must not probe sandbox"); return nil },
		},
	)
	cases := []struct{ method, path string }{
		{"GET", "/v1/chats/default"}, {"HEAD", "/v1/chats/default"}, {"POST", "/v1/chats"},
		{"POST", "/v1/runs"}, {"POST", "/v1/runs/example/resume"}, {"POST", "/v1/runs/example/input"},
		{"GET", "/v1/runs/example/events"}, {"GET", "/v1/users/me/preferences"}, {"GET", "/v1/runs/example"},
	}
	for _, tc := range cases {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			t.Parallel()
			w := request(handler, tc.method, tc.path, "", "", "")
			require.Equal(t, 503, w.Code)
			require.Contains(t, w.Body.String(), "recovery_mode")
		})
	}
	w := request(handler, http.MethodGet, "/readyz", "", "", "")
	require.Equal(t, 503, w.Code)
	require.Contains(t, w.Body.String(), "recovery_mode")
	require.Equal(t, 200, request(handler, http.MethodGet, "/healthz", "", "", "").Code)
}

func TestRecoveryKeepsAuthorizationAndDiagnostics(t *testing.T) {
	t.Parallel()
	auth := authFunc(func(ctx context.Context, r *http.Request) (domain.Principal, error) {
		p, e := allowed(ctx, r)
		p.Permissions["diagnostics.read"] = true
		return p, e
	})
	handler := api.New(
		nil,
		auth,
		api.Options{
			RecoveryMode:      true,
			DiagnosticsHealth: func() domain.DiagnosticsHealth { return domain.DiagnosticsHealth{RecoveryMode: true} },
		},
	)
	w := request(handler, "GET", "/v1/admin/diagnostics/health", "", "", "")
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"recovery_mode":true`)
	denied := api.New(nil, authFunc(func(context.Context, *http.Request) (domain.Principal, error) {
		return domain.Principal{Subject: "alice", OrgID: "org-a"}, nil
	}), api.Options{RecoveryMode: true})
	require.Equal(t, 403, request(denied, "POST", "/v1/runs", "", "", "").Code)
}
