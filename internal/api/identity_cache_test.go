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

func TestCachePurgeRequiresAdministrativeAuthority(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, org  string
		permission bool
		status     int
	}{
		{"allowed", "admin", true, 200},
		{"wrong organization", "other", true, 403},
		{"missing permission", "admin", false, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			called := false
			auth := authFunc(func(context.Context, *http.Request) (domain.Principal, error) {
				return domain.Principal{
					Subject:     "user",
					OrgID:       tc.org,
					Permissions: map[string]bool{"settings.write": tc.permission},
				}, nil
			})
			h := api.New(
				nil,
				auth,
				api.Options{
					RecoveryMode:        true,
					AdminOrganizationID: "admin",
					ClearIdentityCache:  func(context.Context) (int64, error) { called = true; return 42, nil },
				},
			)
			w := request(h, http.MethodPost, "/v1/admin/identity/cache/clear", "", "", "")
			require.Equal(t, tc.status, w.Code)
			require.Equal(t, tc.status == 200, called)
			if called {
				require.JSONEq(t, `{"generation":42}`, w.Body.String())
			}
		})
	}
}

func TestCachePurgeBypassesWarmAuthority(t *testing.T) {
	t.Parallel()
	var revoked, down atomic.Bool
	var iam *httptest.Server
	iam = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/openid-configuration" {
			_ = json.NewEncoder(w).
				Encode(map[string]string{"issuer": iam.URL, "introspection_endpoint": iam.URL + "/introspect", "jwks_uri": iam.URL + "/keys"})
			return
		}
		if down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).
			Encode(map[string]any{"active": !revoked.Load(), "iss": iam.URL, "sub": "user", "aud": "harness", "exp": time.Now().Add(time.Hour).Unix(), "urn:zitadel:iam:user:resourceowner:id": "admin", "urn:zitadel:iam:org:project:roles": map[string]any{"admin": map[string]string{"admin": "example"}}})
	}))
	defer iam.Close()
	auth, err := identity.New(
		config.Identity{
			Issuer:                     iam.URL,
			Audience:                   "harness",
			ClientID:                   "client",
			ClientSecret:               "secret",
			JWKSCacheTTLSeconds:        60,
			PermissionsCacheTTLSeconds: 120,
			RolePermissions:            map[string][]string{"admin": {"settings.write"}},
		},
		iam.Client(),
		nil,
	)
	require.NoError(t, err)
	r := httptest.NewRequest(http.MethodPost, "/v1/admin/identity/cache/clear", nil)
	r.Header.Set("Authorization", "Bearer token")
	_, err = auth.Authenticate(context.Background(), r)
	require.NoError(t, err)
	called := false
	h := api.New(
		nil,
		auth,
		api.Options{
			AdminOrganizationID: "admin",
			ClearIdentityCache:  func(context.Context) (int64, error) { called = true; return 1, nil },
		},
	)
	revoked.Store(true)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.False(t, called)
	revoked.Store(false)
	down.Store(true)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), `"retryable":true`)
	require.False(t, called)
}
