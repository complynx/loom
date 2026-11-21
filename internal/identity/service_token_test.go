package identity_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/identity"
)

// A token service is not trusted for the facts of its token. The token must come
// from the issuer, belong to the same user and organization and never open Loom.
func TestServiceTokenBindsUserAndNeverOpensLoom(t *testing.T) {
	t.Parallel()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "key"),
	)
	require.NoError(t, err)
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	forger, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: other},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "key"),
	)
	require.NoError(t, err)
	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"issuer": issuer.URL, "jwks_uri": issuer.URL + "/keys",
				"introspection_endpoint": issuer.URL + "/introspect",
			})
		case "/keys":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{
				Keys: []jose.JSONWebKey{{Key: &key.PublicKey, Use: "sig", KeyID: "key"}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer issuer.Close()
	auth, err := identity.New(config.Identity{
		Issuer: issuer.URL, Audience: "mcp", ClientID: "client", ClientSecret: "secret", JWKSCacheTTLSeconds: 60,
	}, issuer.Client(), nil)
	require.NoError(t, err)

	// The token service answers by the agent's token: each name is one fault.
	var requests atomic.Int32
	fetch := func(_ context.Context, service, subjectToken string) (string, *int64, error) {
		requests.Add(1)
		// The issuer's clock is a little ahead, as it can be on a real installation.
		claims := jwt.Claims{
			Issuer: issuer.URL, Subject: "user", Audience: jwt.Audience{service},
			IssuedAt: jwt.NewNumericDate(time.Now().Add(5 * time.Second)),
			Expiry:   jwt.NewNumericDate(time.Now().Add(time.Hour)),
		}
		lifetime, sign, org := new(int64(3600)), signer, "org"
		switch subjectToken {
		case "other-user":
			claims.Subject = "mallory"
		case "opens-loom":
			claims.Audience = jwt.Audience{service, "harness"}
		case "opens-mcp":
			claims.Audience = jwt.Audience{service, "mcp"}
		case "other-issuer":
			claims.Issuer = "https://other.example"
		case "expired":
			claims.Expiry = jwt.NewNumericDate(time.Now().Add(-time.Second))
		case "forged":
			sign = forger
		case "other-org":
			org = "other"
		case "zero-lifetime":
			lifetime = new(int64(0))
		case "no-lifetime":
			lifetime = nil
		case "empty":
			return "", lifetime, nil
		case "opaque":
			return "not-a-jwt", lifetime, nil
		}
		token, signErr := jwt.Signed(sign).Claims(claims).
			Claims(map[string]any{"urn:zitadel:iam:user:resourceowner:id": org}).Serialize()
		assert.NoError(t, signErr)
		return token, lifetime, nil
	}
	tokens := identity.NewServiceTokens(auth, identity.ServiceTokenSource{
		Fetch: fetch, Forbidden: []string{"harness", "mcp"},
	})
	caller := func(token string) domain.ExternalCaller {
		return domain.ExternalCaller{
			Principal: domain.Principal{
				Issuer: issuer.URL, Subject: "user", OrgID: "org", ExpiresAt: time.Now().Add(time.Hour),
			},
			Token: token,
		}
	}
	fetched := func(held domain.ExternalCaller, service string) int32 {
		before := requests.Load()
		_, fetchErr := tokens.For(context.Background(), held, service)
		require.NoError(t, fetchErr, held.Token)
		return requests.Load() - before
	}

	service, err := tokens.For(context.Background(), caller("agent"), "chat")
	require.NoError(t, err)
	require.NotEmpty(t, service)
	require.NotEqual(t, "agent", service, "the agent's token must not reach a service")
	again, err := tokens.For(context.Background(), caller("agent"), "chat")
	require.NoError(t, err)
	require.Equal(t, service, again)
	require.EqualValues(t, 1, requests.Load(), "a valid service token is reused")
	// Another token service gives other rights: its token is never shared.
	require.EqualValues(t, 1, fetched(caller("agent"), "other"))

	for _, token := range []string{
		"other-user", "other-org", "opens-loom", "opens-mcp", "other-issuer", "expired", "forged",
		"zero-lifetime", "empty", "opaque",
	} {
		_, err = tokens.For(context.Background(), caller(token), "chat")
		require.ErrorIs(t, err, identity.ErrUnauthorized, token)
	}

	// The stated lifetime is optional; the signed expiry then bounds the reuse.
	require.EqualValues(t, 1, fetched(caller("no-lifetime"), "chat"))
	require.EqualValues(t, 0, fetched(caller("no-lifetime"), "chat"))
	// After a role change of the same agent token, the service gets a token with the new roles.
	granted := caller("agent")
	granted.Principal.Permissions = map[string]bool{"notes.read": true}
	require.EqualValues(t, 1, fetched(granted, "chat"))
	require.EqualValues(t, 0, fetched(granted, "chat"))
	// A service token is never reused beyond the agent's own token.
	closing := caller("closing")
	closing.Principal.ExpiresAt = time.Now().Add(10 * time.Second)
	require.EqualValues(t, 1, fetched(closing, "chat"))
	require.EqualValues(t, 1, fetched(closing, "chat"))
}
