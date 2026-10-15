package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/api"
	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/identity"
)

func operatorIdentityFixture(t *testing.T) (*identity.Authenticator, *atomic.Bool) {
	t.Helper()
	removed := &atomic.Bool{}
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
			"active": token != "revoked", "iss": issuer, "sub": "user", "aud": "project",
			"exp": time.Now().Add(time.Hour).Unix(),
		}
		org := "admin-org"
		if token == "foreign-grant" {
			org = "foreign-org"
		}
		if token != "no-roles" && !removed.Load() {
			claims["urn:zitadel:iam:org:project:roles"] = map[string]any{"admin": map[string]string{org: "example"}}
		}
		switch token {
		case "machine":
			claims["sub"] = "service"
		case "discovered-machine":
			claims["sub"] = "discovered-service"
		case "wrong-audience":
			claims["aud"] = "other-project"
		case "expired":
			claims["exp"] = time.Now().Add(-time.Hour).Unix()
		case "foreign-owner":
			claims["urn:zitadel:iam:user:resourceowner:id"] = "foreign-org"
		case "normal-api":
			claims["urn:zitadel:iam:user:resourceowner:id"] = "admin-org"
		}
		_ = json.NewEncoder(w).Encode(claims)
	}))
	t.Cleanup(peer.Close)
	issuer = peer.URL
	auth, err := identity.New(config.Identity{
		Issuer: issuer, Audience: "project", ClientID: "api", ClientSecret: "test-only",
		AdminOrganizationID: "admin-org", PermissionsCacheTTLSeconds: 120,
		ServiceSubjects: []string{"service"}, RolePermissions: map[string][]string{"admin": {"diagnostics.read"}},
	}, peer.Client(), nil)
	require.NoError(t, err)
	require.NoError(t, auth.UseServiceClassifier(func(_ context.Context, p domain.Principal) (bool, error) {
		return p.Subject == "discovered-service", nil
	}))
	return auth, removed
}

func TestBrowserOrganizationComesOnlyFromConfiguredAdminScope(t *testing.T) {
	t.Parallel()
	auth, removed := operatorIdentityFixture(t)
	handler := api.New(nil, auth, api.Options{
		OperatorProxyOrigin: "https://panel.example", AdminOrganizationID: "admin-org",
		DiagnosticsHealth: func() domain.DiagnosticsHealth { return domain.DiagnosticsHealth{} },
	})
	for _, tc := range []struct {
		token, path, subjectToken, session string
		status                             int
	}{
		{"admin", "/admin/api/diagnostics/health", "", "", 200},
		{"admin", "/v1/admin/diagnostics/health", "", "", 401},
		{"normal-api", "/v1/admin/diagnostics/health", "", "", 200},
		{"foreign-owner", "/admin/api/diagnostics/health", "", "", 200},
		{"foreign-grant", "/admin/api/diagnostics/health", "", "", 403},
		{"no-roles", "/admin/api/diagnostics/health", "", "", 403},
		{"wrong-audience", "/admin/api/diagnostics/health", "", "", 401},
		{"expired", "/admin/api/diagnostics/health", "", "", 401},
		{"revoked", "/admin/api/diagnostics/health", "", "", 401},
		{"machine", "/admin/api/diagnostics/health", "", "", 401},
		{"discovered-machine", "/admin/api/diagnostics/health", "", "", 401},
		{"admin", "/admin/api/diagnostics/health", "delegated-user", "", 401},
		{"admin", "/admin/api/diagnostics/health", "", "session-id", 401},
	} {
		r := httptest.NewRequest(http.MethodGet, tc.path, nil)
		r.Header.Set("Authorization", "Bearer "+tc.token)
		r.Header.Set("X-Subject-Token", tc.subjectToken)
		r.Header.Set("X-Auth-Session-Id", tc.session)
		r.Header.Set("X-Organization-Id", "admin-org")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		require.Equal(t, tc.status, w.Code, "%s %s: %s", tc.token, tc.path, w.Body.String())
	}
	removed.Store(true)
	_, err := auth.ClearPermissionsCache(t.Context())
	require.NoError(t, err)
	r := httptest.NewRequest(http.MethodGet, "/admin/api/diagnostics/health", nil)
	r.Header.Set("Authorization", "Bearer admin")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	require.Equal(t, http.StatusForbidden, w.Code, "purging cached authority must expose removed roles")
	require.NoError(t, auth.UseServiceClassifier(func(context.Context, domain.Principal) (bool, error) {
		return false, identity.ErrUnavailable
	}))
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
}
