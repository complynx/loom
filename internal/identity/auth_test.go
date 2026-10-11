package identity_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/identity"
)

func TestAuthenticationAccessAndDelegation(t *testing.T) {
	t.Parallel()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	var issuer string
	server := authenticationServer(t, key, &issuer)
	defer server.Close()
	issuer = server.URL
	cfg := config.Identity{
		Issuer:              issuer,
		Audience:            "harness",
		ClientID:            "client",
		ClientSecret:        "secret",
		JWKSCacheTTLSeconds: 60,
		ServiceSubjects:     []string{"svc"},
		RolePermissions:     map[string][]string{"member": {"chat.read", "runs.create", "massage.read"}},
	}
	auth, err := identity.New(
		cfg,
		server.Client(),
		func(context.Context, *http.Request, domain.Principal) (identity.Binding, error) {
			return identity.Binding{
				Actor:       "svc",
				Permissions: map[string]bool{"chat.read": true, "runs.create": true},
			}, nil
		},
	)
	require.NoError(t, err)
	request := func(token, delegated string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "http://harness/runs", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Subject-Token", delegated)
		return r
	}
	p, err := auth.Authenticate(context.Background(), request("opaque", ""))
	if err != nil || p.Subject != "user" || !p.Has("runs.create") || p.Has("settings.write") {
		t.Fatalf("valid user/permissions: %v %+v", err, p)
	}
	for _, token := range []string{"expired", "revoked", "wrong-audience"} {
		if _, authenticateErr := auth.Authenticate(
			context.Background(),
			request(token, ""),
		); !errors.Is(
			authenticateErr,
			identity.ErrUnauthorized,
		) {
			t.Errorf("%s accepted: %v", token, authenticateErr)
		}
	}
	for _, tc := range []struct{ token, delegate string }{{"service", ""}, {"opaque", "opaque"}, {"service", "other-org"}} {
		if _, authenticateErr := auth.Authenticate(
			context.Background(),
			request(tc.token, tc.delegate),
		); !errors.Is(
			authenticateErr,
			identity.ErrUnauthorized,
		) {
			t.Errorf("invalid delegation accepted: %v", authenticateErr)
		}
	}
	delegated, err := auth.Authenticate(context.Background(), request("service", "opaque"))
	if err != nil || delegated.Actor != "svc" || delegated.Subject != "user" ||
		!delegated.Has("massage.read") {
		t.Fatalf("delegation: %v %+v", err, delegated)
	}
	checkDelegatedSessions(t, auth, delegated, request)
	checkSignedAuthentication(t, auth, key, issuer, request)
}

func TestSessionCipherRejectsTamperingAndDifferentPrincipal(t *testing.T) {
	t.Parallel()
	c, err := identity.NewSessionCipher("key", make([]byte, 32))
	require.NoError(t, err)
	p := domain.Principal{
		Issuer:    "issuer",
		Subject:   "user",
		Actor:     "user",
		OrgID:     "org",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	sealed, err := c.Seal(p, "private-token")
	require.NoError(t, err)
	if strings.Contains(string(sealed), "private-token") {
		t.Fatal("plaintext leaked")
	}
	token, err := c.Open(p, sealed)
	if err != nil || token != "private-token" {
		t.Fatal(err)
	}
	other := p
	other.Subject = "victim"
	if _, openErr := c.Open(other, sealed); !errors.Is(openErr, identity.ErrUnauthorized) {
		t.Fatal("different principal accepted")
	}
	tampered := json.RawMessage(strings.Replace(string(sealed), "\"org\"", "\"evil\"", 1))
	if _, openErr := c.Open(p, tampered); !errors.Is(openErr, identity.ErrUnauthorized) {
		t.Fatal("tampered binding accepted")
	}
}

func authenticationServer(t *testing.T, key *rsa.PrivateKey, issuer *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).
				Encode(map[string]any{"issuer": *issuer, "jwks_uri": *issuer + "/keys", "introspection_endpoint": *issuer + "/introspect"})
		case "/keys":
			_ = json.NewEncoder(w).
				Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, Use: "sig", KeyID: "key"}}})
		case "/introspect":
			user, pass, ok := r.BasicAuth()
			if !ok || user != "client" || pass != "secret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if parseFormErr := r.ParseForm(); parseFormErr != nil {
				t.Error(parseFormErr)
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
			_ = json.NewEncoder(w).
				Encode(map[string]any{"active": active, "iss": *issuer, "sub": sub, "aud": aud, "exp": exp, "urn:zitadel:iam:user:resourceowner:id": org, "urn:zitadel:iam:org:project:roles": map[string]any{"member": map[string]string{org: "example"}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func checkSignedAuthentication(
	t *testing.T,
	auth *identity.Authenticator,
	key *rsa.PrivateKey,
	issuer string,
	request func(string, string) *http.Request,
) {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, iss, aud string
		expiry         time.Time
	}{{"valid", issuer, "harness", time.Now().Add(time.Hour)}, {"issuer", "https://untrusted.invalid", "harness", time.Now().Add(time.Hour)}, {"aud", issuer, "wrong", time.Now().Add(time.Hour)}, {"expiry", issuer, "harness", time.Now().Add(-time.Hour)}} {
		token, serializeErr := jwt.Signed(signer).
			Claims(jwt.Claims{Issuer: tc.iss, Subject: "user", Audience: jwt.Audience{tc.aud}, Expiry: jwt.NewNumericDate(tc.expiry)}).
			Serialize()
		if serializeErr != nil {
			t.Fatal(serializeErr)
		}
		_, serializeErr = auth.Authenticate(context.Background(), request(token, ""))
		if tc.name == "valid" && serializeErr != nil {
			t.Errorf("valid JWT rejected: %v", serializeErr)
		}
		if tc.name != "valid" && !errors.Is(serializeErr, identity.ErrUnauthorized) {
			t.Errorf("%s accepted %v", tc.name, serializeErr)
		}
	}
}

func checkDelegatedSessions(
	t *testing.T,
	auth *identity.Authenticator,
	delegated domain.Principal,
	request func(string, string) *http.Request,
) {
	t.Helper()
	cipher, err := identity.NewSessionCipher("key", make([]byte, 32))
	require.NoError(t, err)
	sealed, err := cipher.SealRequest(context.Background(), request("service", "opaque"), delegated)
	require.NoError(t, err)
	fresh, token, err := auth.VerifySession(
		context.Background(),
		cipher,
		delegated,
		sealed,
		func(context.Context, domain.Principal) (identity.Binding, error) {
			return identity.Binding{
				Actor:       "svc",
				Permissions: map[string]bool{"chat.read": true, "runs.create": true},
			}, nil
		},
	)
	if err != nil || fresh.Actor != "svc" || token != "opaque" {
		t.Fatalf("session resume: %v", err)
	}
	if _, _, verifySessionErr := auth.VerifySession(
		context.Background(),
		cipher,
		delegated,
		sealed,
		nil,
	); !errors.Is(
		verifySessionErr,
		identity.ErrUnauthorized,
	) {
		t.Fatal("unregistered service session accepted")
	}
	if useSessionsErr := auth.UseSessions(
		cipher,
		func(_ context.Context, id string, actor domain.Principal) (domain.Principal, json.RawMessage, error) {
			if id != "saved" || actor.Subject != "svc" {
				return domain.Principal{}, nil, identity.ErrUnauthorized
			}
			return delegated, sealed, nil
		},
		func(context.Context, domain.Principal) (identity.Binding, error) {
			return identity.Binding{
				Actor:       "svc",
				Permissions: map[string]bool{"chat.read": true, "runs.create": true},
			}, nil
		},
	); useSessionsErr != nil {
		t.Fatal(useSessionsErr)
	}
	reused := request("service", "")
	reused.Header.Set("X-Auth-Session-Id", "saved")
	restored, err := auth.Authenticate(context.Background(), reused)
	if err != nil || restored.Actor != "svc" || restored.Subject != "user" || !restored.Has("massage.read") {
		t.Fatalf("header session reuse: %v %+v", err, restored)
	}
	reused.Header.Set("X-Auth-Session-Id", "victim-session")
	if _, authenticateErr := auth.Authenticate(
		context.Background(),
		reused,
	); !errors.Is(
		authenticateErr,
		identity.ErrUnauthorized,
	) {
		t.Fatal("unknown session accepted")
	}
	reused.Header.Set("X-Auth-Session-Id", "saved")
	reused.Header.Set("X-Subject-Token", "opaque")
	if _, authenticateErr := auth.Authenticate(
		context.Background(),
		reused,
	); !errors.Is(
		authenticateErr,
		identity.ErrUnauthorized,
	) {
		t.Fatal("ambiguous delegation accepted")
	}
}
