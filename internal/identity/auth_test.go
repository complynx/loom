package identity

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"github.com/complynx/clx-scripter/internal/config"
	"github.com/complynx/clx-scripter/internal/domain"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAuthenticationAccessAndDelegation(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "jwks_uri": issuer + "/keys", "introspection_endpoint": issuer + "/introspect"})
		case "/keys":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, Use: "sig", KeyID: "key"}}})
		case "/introspect":
			user, pass, ok := r.BasicAuth()
			if !ok || user != "client" || pass != "secret" {
				w.WriteHeader(401)
				return
			}
			if e := r.ParseForm(); e != nil {
				t.Error(e)
			}
			token := r.Form.Get("token")
			sub := "user"
			org := "org"
			active := true
			aud := "harness"
			exp := time.Now().Add(time.Hour).Unix()
			switch token {
			case "expired":
				exp = time.Now().Add(-time.Hour).Unix()
			case "revoked":
				active = false
			case "wrong-audience":
				aud = "other"
			case "service":
				sub = "svc"
			case "other-org":
				org = "other"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"active": active, "iss": issuer, "sub": sub, "aud": aud, "exp": exp, "urn:zitadel:iam:user:resourceowner:id": org, "urn:zitadel:iam:org:project:roles": map[string]any{"member": map[string]string{org: "example"}}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	issuer = server.URL
	cfg := config.Identity{Issuer: issuer, Audience: "harness", ClientID: "client", ClientSecret: "secret", JWKSCacheTTLSeconds: 60, ServiceSubjects: []string{"svc"}, RolePermissions: map[string][]string{"member": {"chat.read", "runs.create", "massage.read"}}}
	auth, e := New(cfg, server.Client(), func(context.Context, *http.Request, domain.Principal) (Binding, error) {
		return Binding{Actor: "svc", Permissions: map[string]bool{"chat.read": true, "runs.create": true}}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	request := func(token, delegated string) *http.Request {
		r := httptest.NewRequest("POST", "http://harness/runs", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Subject-Token", delegated)
		return r
	}
	p, e := auth.Authenticate(context.Background(), request("opaque", ""))
	if e != nil || p.Subject != "user" || !p.Has("runs.create") || p.Has("settings.write") {
		t.Fatalf("valid user/permissions: %v %+v", e, p)
	}
	for _, token := range []string{"expired", "revoked", "wrong-audience"} {
		if _, e := auth.Authenticate(context.Background(), request(token, "")); !errors.Is(e, ErrUnauthorized) {
			t.Errorf("%s accepted: %v", token, e)
		}
	}
	for _, tc := range []struct{ token, delegate string }{{"service", ""}, {"opaque", "opaque"}, {"service", "other-org"}} {
		if _, e := auth.Authenticate(context.Background(), request(tc.token, tc.delegate)); !errors.Is(e, ErrUnauthorized) {
			t.Errorf("invalid delegation accepted: %v", e)
		}
	}
	delegated, e := auth.Authenticate(context.Background(), request("service", "opaque"))
	if e != nil || delegated.Actor != "svc" || delegated.Subject != "user" || !delegated.Has("massage.read") {
		t.Fatalf("delegation: %v %+v", e, delegated)
	}
	cipher, e := NewSessionCipher("key", make([]byte, 32))
	if e != nil {
		t.Fatal(e)
	}
	sealed, e := cipher.SealRequest(context.Background(), request("service", "opaque"), delegated)
	if e != nil {
		t.Fatal(e)
	}
	fresh, token, e := auth.VerifySession(context.Background(), cipher, delegated, sealed, func(context.Context, domain.Principal) (Binding, error) {
		return Binding{Actor: "svc", Permissions: map[string]bool{"chat.read": true, "runs.create": true}}, nil
	})
	if e != nil || fresh.Actor != "svc" || token != "opaque" {
		t.Fatalf("session resume: %v", e)
	}
	if _, _, e := auth.VerifySession(context.Background(), cipher, delegated, sealed, nil); !errors.Is(e, ErrUnauthorized) {
		t.Fatal("unregistered service session accepted")
	}
	if e := auth.UseSessions(cipher, func(_ context.Context, id string, actor domain.Principal) (domain.Principal, json.RawMessage, error) {
		if id != "saved" || actor.Subject != "svc" {
			return domain.Principal{}, nil, ErrUnauthorized
		}
		return delegated, sealed, nil
	}, func(context.Context, domain.Principal) (Binding, error) {
		return Binding{Actor: "svc", Permissions: map[string]bool{"chat.read": true, "runs.create": true}}, nil
	}); e != nil {
		t.Fatal(e)
	}
	reused := request("service", "")
	reused.Header.Set("X-Auth-Session-ID", "saved")
	restored, e := auth.Authenticate(context.Background(), reused)
	if e != nil || restored.Actor != "svc" || restored.Subject != "user" || !restored.Has("massage.read") {
		t.Fatalf("header session reuse: %v %+v", e, restored)
	}
	reused.Header.Set("X-Auth-Session-ID", "victim-session")
	if _, e := auth.Authenticate(context.Background(), reused); !errors.Is(e, ErrUnauthorized) {
		t.Fatal("unknown session accepted")
	}
	reused.Header.Set("X-Auth-Session-ID", "saved")
	reused.Header.Set("X-Subject-Token", "opaque")
	if _, e := auth.Authenticate(context.Background(), reused); !errors.Is(e, ErrUnauthorized) {
		t.Fatal("ambiguous delegation accepted")
	}
	signer, e := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT"))
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name, iss, aud string
		expiry         time.Time
	}{{"valid", issuer, "harness", time.Now().Add(time.Hour)}, {"issuer", "https://untrusted.invalid", "harness", time.Now().Add(time.Hour)}, {"aud", issuer, "wrong", time.Now().Add(time.Hour)}, {"expiry", issuer, "harness", time.Now().Add(-time.Hour)}} {
		token, e := jwt.Signed(signer).Claims(jwt.Claims{Issuer: tc.iss, Subject: "user", Audience: jwt.Audience{tc.aud}, Expiry: jwt.NewNumericDate(tc.expiry)}).Serialize()
		if e != nil {
			t.Fatal(e)
		}
		_, e = auth.Authenticate(context.Background(), request(token, ""))
		if tc.name == "valid" && e != nil {
			t.Errorf("valid JWT rejected: %v", e)
		}
		if tc.name != "valid" && !errors.Is(e, ErrUnauthorized) {
			t.Errorf("%s accepted %v", tc.name, e)
		}
	}
}
func TestSessionCipherRejectsTamperingAndDifferentPrincipal(t *testing.T) {
	c, e := NewSessionCipher("key", make([]byte, 32))
	if e != nil {
		t.Fatal(e)
	}
	p := domain.Principal{Issuer: "issuer", Subject: "user", Actor: "user", OrgID: "org", ExpiresAt: time.Now().Add(time.Hour)}
	sealed, e := c.Seal(p, "private-token")
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(sealed), "private-token") {
		t.Fatal("plaintext leaked")
	}
	token, e := c.Open(p, sealed)
	if e != nil || token != "private-token" {
		t.Fatal(e)
	}
	other := p
	other.Subject = "victim"
	if _, e := c.Open(other, sealed); !errors.Is(e, ErrUnauthorized) {
		t.Fatal("different principal accepted")
	}
	tampered := json.RawMessage(strings.Replace(string(sealed), "\"org\"", "\"evil\"", 1))
	if _, e := c.Open(p, tampered); !errors.Is(e, ErrUnauthorized) {
		t.Fatal("tampered binding accepted")
	}
}
