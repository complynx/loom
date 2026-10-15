package identity_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/identity"
)

func TestApplicationKeyAuthenticatesIntrospectionWithFreshAssertions(t *testing.T) {
	t.Parallel()
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	data, err := json.Marshal(map[string]string{
		"type":     "application",
		"clientId": "backend",
		"keyId":    "app-key",
		"key": string(
			pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(private)}),
		),
	})
	require.NoError(t, err)
	var issuer string
	requests := make(chan *http.Request, 2)
	seen := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/openid-configuration" {
			_ = json.NewEncoder(w).
				Encode(map[string]string{"issuer": issuer, "jwks_uri": issuer + "/keys", "introspection_endpoint": issuer + "/introspect"})
			return
		}
		if r.URL.Path != "/introspect" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if parseErr := r.ParseForm(); parseErr != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- r
		_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "iss": issuer, "sub": "user", "aud": "loom",
			"exp": time.Now().Add(time.Hour).Unix(), "urn:zitadel:iam:user:resourceowner:id": "org"})
	}))
	defer server.Close()
	issuer = server.URL
	auth, err := identity.New(
		config.Identity{Issuer: issuer, Audience: "loom", ClientKeyBase64: base64.StdEncoding.EncodeToString(data)},
		server.Client(),
		nil,
	)
	require.NoError(t, err)
	for range 2 {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "Bearer opaque-user-token")
		p, authErr := auth.Authenticate(t.Context(), r)
		require.NoError(t, authErr)
		require.Equal(t, "user", p.Subject)
		assertApplicationRequest(t, <-requests, private, issuer, seen)
	}
	require.Len(t, seen, 2)
}

func assertApplicationRequest(
	t *testing.T,
	r *http.Request,
	key *rsa.PrivateKey,
	issuer string,
	seen map[string]bool,
) {
	t.Helper()
	require.Empty(t, r.Header.Get("Authorization"))
	require.NoError(t, r.ParseForm())
	require.Equal(t, "opaque-user-token", r.Form.Get("token"))
	require.Equal(t, "urn:ietf:params:oauth:client-assertion-type:jwt-bearer", r.Form.Get("client_assertion_type"))
	parsed, err := jwt.ParseSigned(r.Form.Get("client_assertion"), []jose.SignatureAlgorithm{jose.RS256})
	require.NoError(t, err)
	require.Equal(t, "app-key", parsed.Headers[0].KeyID)
	var claims jwt.Claims
	require.NoError(t, parsed.Claims(&key.PublicKey, &claims))
	require.NoError(
		t,
		claims.Validate(
			jwt.Expected{Issuer: "backend", Subject: "backend", AnyAudience: jwt.Audience{issuer}, Time: time.Now()},
		),
	)
	require.LessOrEqual(t, claims.Expiry.Time().Sub(claims.IssuedAt.Time()), time.Minute)
	require.NotEmpty(t, claims.ID)
	require.False(t, seen[claims.ID])
	seen[claims.ID] = true
}
