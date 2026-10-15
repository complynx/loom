package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/identity"
	"github.com/complynx/loom/internal/telemetry"
)

func TestRuntimeOperatorAuthenticationThroughTelemetry(t *testing.T) {
	t.Parallel()
	var issuer string
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/openid-configuration" {
			_ = json.NewEncoder(w).Encode(map[string]string{
				"issuer": issuer, "jwks_uri": issuer + "/keys", "introspection_endpoint": issuer + "/introspect",
			})
			return
		}
		if r.URL.Path != "/introspect" || r.ParseForm() != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		token := r.Form.Get("token")
		claims := map[string]any{
			"active": token != "revoked", "iss": issuer, "sub": token, "aud": "project",
			"exp": time.Now().Add(time.Hour).Unix(),
		}
		if token != "no-role" {
			claims["urn:zitadel:iam:org:project:roles"] = map[string]any{
				"admin": map[string]string{"org": "example"},
			}
		}
		_ = json.NewEncoder(w).Encode(claims)
	}))
	defer peer.Close()
	issuer = peer.URL
	cfg := config.Config{
		Identity: config.Identity{
			Issuer: issuer, Audience: "project", ClientID: "api", ClientSecret: "test-only",
			AdminOrganizationID: "org", ServiceSubjects: []string{"service"},
			RolePermissions: map[string][]string{"admin": {"diagnostics.read"}},
		},
		Operator: config.Operator{ProxyOrigin: "https://panel.example"},
	}
	auth, err := identity.New(cfg.Identity, peer.Client(), nil)
	require.NoError(t, err)
	cipher, err := identity.NewSessionCipher("test", make([]byte, 32))
	require.NoError(t, err)
	observer := telemetry.New(slog.New(slog.DiscardHandler))
	handler := runtimeHandler(t.Context(), cfg, nil, auth, observer, nil,
		func(context.Context) error { return nil }, cipher, nil, nil)
	for _, tc := range []struct {
		token, path string
		status      int
	}{
		{"admin", "/admin/api/diagnostics/health", http.StatusOK},
		{"", "/admin/api/diagnostics/health", http.StatusUnauthorized},
		{"revoked", "/admin/api/diagnostics/health", http.StatusUnauthorized},
		{"service", "/admin/api/diagnostics/health", http.StatusUnauthorized},
		{"no-role", "/admin/api/diagnostics/health", http.StatusForbidden},
		{"admin", "/v1/admin/diagnostics/health", http.StatusUnauthorized},
	} {
		r := httptest.NewRequest(http.MethodGet, tc.path, nil)
		r.Header.Set("Origin", cfg.Operator.ProxyOrigin)
		if tc.token != "" {
			r.Header.Set("Authorization", "Bearer "+tc.token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		require.Equal(t, tc.status, w.Code, "%s %s: %s", tc.token, tc.path, w.Body.String())
	}
	metrics := httptest.NewRecorder()
	observer.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Contains(t, metrics.Body.String(), `clx_operations_total{component="authorization",outcome="success"} 3`)
	require.Contains(t, metrics.Body.String(), `clx_operations_total{component="authorization",outcome="denied"} 3`)
}
