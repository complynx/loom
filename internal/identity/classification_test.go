package identity

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/complynx/clx-scripter/internal/config"
	"github.com/complynx/clx-scripter/internal/domain"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDynamicServiceClassificationNeverDowngradesRemovedActor(t *testing.T) {
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": issuer, "jwks_uri": issuer + "/keys", "introspection_endpoint": issuer + "/introspect"})
		case "/introspect":
			if e := r.ParseForm(); e != nil {
				t.Error(e)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "iss": issuer, "sub": r.Form.Get("token"), "aud": "harness", "exp": time.Now().Add(time.Hour).Unix(), "urn:zitadel:iam:user:resourceowner:id": "org", "urn:zitadel:iam:org:project:roles": map[string]any{"member": map[string]string{"org": "example"}}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	issuer = server.URL
	cfg := config.Identity{Issuer: issuer, Audience: "harness", ClientID: "client", ClientSecret: "secret", RolePermissions: map[string][]string{"member": {"chat.read"}}}
	ready, registered := true, true
	known := map[string]bool{"service": true}
	classifier := func(_ context.Context, p domain.Principal) (bool, error) {
		if !ready {
			return false, ErrUnavailable
		}
		return known[p.Subject], nil
	}
	binding := func(_ context.Context, _ *http.Request, p domain.Principal) (Binding, error) {
		if !registered || p.Subject != "service" {
			return Binding{}, ErrUnauthorized
		}
		return Binding{Actor: "service", Permissions: map[string]bool{"chat.read": true}}, nil
	}
	newAuth := func() *Authenticator {
		a, e := New(cfg, server.Client(), binding)
		if e != nil {
			t.Fatal(e)
		}
		if e := a.UseServiceClassifier(classifier); e != nil {
			t.Fatal(e)
		}
		return a
	}
	request := func(token, delegate string) *http.Request {
		r := httptest.NewRequest("GET", "http://harness/history", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Subject-Token", delegate)
		return r
	}
	a := newAuth()
	p, e := a.Authenticate(context.Background(), request("service", "user"))
	if e != nil || p.Subject != "user" || p.Actor != "service" {
		t.Fatalf("discovered caller without service_subjects: %v %+v", e, p)
	}
	registered = false
	for _, auth := range []*Authenticator{a, newAuth()} {
		for _, delegate := range []string{"", "user"} {
			if _, e := auth.Authenticate(context.Background(), request("service", delegate)); !errors.Is(e, ErrUnauthorized) {
				t.Fatalf("removed actor downgraded after restart: %v", e)
			}
		}
	}
	ready = false
	if _, e := a.Authenticate(context.Background(), request("user", "")); !errors.Is(e, ErrUnavailable) {
		t.Fatalf("stale classification source accepted user: %v", e)
	}
}
