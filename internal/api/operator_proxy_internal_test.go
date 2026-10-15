package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/identity"
)

type proxyTestAuth struct{}

func (a proxyTestAuth) AuthenticateOperator(ctx context.Context, r *http.Request) (domain.Principal, error) {
	return a.Authenticate(ctx, r)
}

func (proxyTestAuth) Authenticate(_ context.Context, r *http.Request) (domain.Principal, error) {
	if r.Header.Get("Authorization") != "Bearer valid-user-token" {
		return domain.Principal{}, identity.ErrUnauthorized
	}
	return domain.Principal{Subject: "operator", OrgID: "org", Permissions: map[string]bool{
		diagnosticsPermission: r.Header.Get("X-Test-Denied") == "",
	}}, nil
}

func TestOperatorProxyPreservesBearerAuthorizationAndIsolation(t *testing.T) {
	t.Parallel()
	const origin = "https://panel.example"
	handler := New(
		nil,
		proxyTestAuth{},
		Options{
			OperatorProxyOrigin: origin,
			AdminOrganizationID: "org",
			DiagnosticsHealth: func() domain.DiagnosticsHealth {
				return domain.DiagnosticsHealth{Dependencies: map[string]bool{"database": true}}
			},
		},
	)
	for _, tc := range []struct {
		name, method, path, token, origin, site string
		denied                                  bool
		status                                  int
	}{
		{"browser", "GET", "/admin/api/diagnostics/health", "Bearer valid-user-token", origin, "same-origin", false, 200},
		{"head", "HEAD", "/admin/api/diagnostics/health", "Bearer valid-user-token", "", "same-origin", false, 200},
		{"cookie alone", "GET", "/admin/api/diagnostics/health", "", origin, "same-origin", false, 401},
		{"permission removed", "GET", "/admin/api/diagnostics/health", "Bearer valid-user-token", origin, "same-origin", true, 403},
		{"foreign origin", "GET", "/admin/api/diagnostics/health", "Bearer valid-user-token", "https://other.example", "same-site", false, 403},
		{"cross-site", "GET", "/admin/api/diagnostics/health", "Bearer valid-user-token", "", "cross-site", false, 403},
		{"write", "POST", "/admin/api/diagnostics", "Bearer valid-user-token", origin, "same-origin", false, 405},
		{"other admin", "GET", "/admin/api/settings", "Bearer valid-user-token", origin, "same-origin", false, 404},
		{"encoded traversal", "GET", "/admin/api/diagnostics/runs/%2e%2e%2f%2e%2e%2fsettings", "Bearer valid-user-token", origin, "same-origin", false, 404},
		{"machine API", "GET", "/v1/admin/diagnostics/health", "Bearer valid-user-token", "", "", false, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(tc.method, origin+tc.path, nil)
			r.Header.Set("Authorization", tc.token)
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Sec-Fetch-Site", tc.site)
			r.Header.Set("X-Forwarded-User", "administrator")
			r.AddCookie(&http.Cookie{Name: "TraefikOidcAuth.Session", Value: "not-a-bearer"})
			if tc.denied {
				r.Header.Set("X-Test-Denied", "true")
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			require.Equal(t, tc.status, w.Code, w.Body.String())
			require.Empty(t, w.Header().Get("Location"))
			if tc.status == http.StatusOK {
				require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestOperatorProxyIsExplicitAndAvailableDuringRecovery(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{false, true} {
		options := Options{
			AdminOrganizationID: "org",
			RecoveryMode:        true,
			DiagnosticsHealth:   func() domain.DiagnosticsHealth { return domain.DiagnosticsHealth{} },
		}
		if enabled {
			options.OperatorProxyOrigin = "https://panel.example"
		}
		handler := New(nil, proxyTestAuth{}, options)
		r := httptest.NewRequest(http.MethodGet, "/admin/api/diagnostics/health", nil)
		r.Header.Set("Authorization", "Bearer valid-user-token")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if enabled {
			require.Equal(t, http.StatusOK, w.Code)
		} else {
			require.Equal(t, http.StatusNotFound, w.Code)
		}
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/login/config", nil))
		if enabled {
			require.Contains(t, w.Body.String(), `"proxy":true`)
			require.Contains(t, w.Body.String(), `"enabled":true`)
		}
	}
}
