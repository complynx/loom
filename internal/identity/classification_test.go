package identity_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/clx-scripter/internal/config"
	"github.com/complynx/clx-scripter/internal/domain"
	"github.com/complynx/clx-scripter/internal/identity"
)

func TestDynamicServiceClassificationNeverDowngradesRemovedActor(t *testing.T) {
	t.Parallel()
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).
				Encode(map[string]string{"issuer": issuer, "jwks_uri": issuer + "/keys", "introspection_endpoint": issuer + "/introspect"})
		case "/introspect":
			if parseFormErr := r.ParseForm(); parseFormErr != nil {
				t.Error(parseFormErr)
			}
			_ = json.NewEncoder(w).
				Encode(map[string]any{"active": true, "iss": issuer, "sub": r.Form.Get("token"), "aud": "harness", "exp": time.Now().Add(time.Hour).Unix(), "urn:zitadel:iam:user:resourceowner:id": "org", "urn:zitadel:iam:org:project:roles": map[string]any{"member": map[string]string{"org": "example"}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	issuer = server.URL
	cfg := config.Identity{
		Issuer:          issuer,
		Audience:        "harness",
		ClientID:        "client",
		ClientSecret:    "secret",
		RolePermissions: map[string][]string{"member": {"chat.read"}},
	}
	ready, registered := true, true
	known := map[string]bool{"service": true}
	classifier := func(_ context.Context, p domain.Principal) (bool, error) {
		if !ready {
			return false, identity.ErrUnavailable
		}
		return known[p.Subject], nil
	}
	binding := func(_ context.Context, _ *http.Request, p domain.Principal) (identity.Binding, error) {
		if !registered || p.Subject != "service" {
			return identity.Binding{}, identity.ErrUnauthorized
		}
		return identity.Binding{Actor: "service", Permissions: map[string]bool{"chat.read": true}}, nil
	}
	newAuth := func() *identity.Authenticator {
		a, newErr := identity.New(cfg, server.Client(), binding)
		require.NoError(t, newErr)
		if useServiceClassifierErr := a.UseServiceClassifier(classifier); useServiceClassifierErr != nil {
			t.Fatal(useServiceClassifierErr)
		}
		return a
	}
	request := func(token, delegate string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://harness/history", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Subject-Token", delegate)
		return r
	}
	a := newAuth()
	p, err := a.Authenticate(context.Background(), request("service", "user"))
	if err != nil || p.Subject != "user" || p.Actor != "service" {
		t.Fatalf("discovered caller without service_subjects: %v %+v", err, p)
	}
	registered = false
	checkRemovedActor(t, []*identity.Authenticator{a, newAuth()}, request)
	ready = false
	if _, authenticateErr := a.Authenticate(
		context.Background(),
		request("user", ""),
	); !errors.Is(
		authenticateErr,
		identity.ErrUnavailable,
	) {
		t.Fatalf("stale classification source accepted user: %v", authenticateErr)
	}
}

func checkRemovedActor(
	t *testing.T,
	authenticators []*identity.Authenticator,
	request func(string, string) *http.Request,
) {
	t.Helper()
	for _, auth := range authenticators {
		for _, delegate := range []string{"", "user"} {
			if _, authenticateErr := auth.Authenticate(
				context.Background(),
				request("service", delegate),
			); !errors.Is(
				authenticateErr,
				identity.ErrUnauthorized,
			) {
				t.Fatalf("removed actor downgraded after restart: %v", authenticateErr)
			}
		}
	}
}
