package identity_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// The service token must come from the issuer, belong to the same user, name the
// requested services and never open Loom. The agent's token is used only to get it.
func TestServiceTokenExchangeBindsUserAndAudiences(t *testing.T) {
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
	var exchanges atomic.Int32
	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"issuer": issuer.URL, "jwks_uri": issuer.URL + "/keys",
				"introspection_endpoint": issuer.URL + "/introspect", "token_endpoint": issuer.URL + "/token",
			})
		case "/keys":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{
				Keys: []jose.JSONWebKey{{Key: &key.PublicKey, Use: "sig", KeyID: "key"}},
			})
		case "/token":
			exchanges.Add(1)
			client, secret, _ := r.BasicAuth()
			client, _ = url.QueryUnescape(client)
			secret, _ = url.QueryUnescape(secret)
			assert.NoError(t, r.ParseForm())
			assert.Equal(t, "exchange@project se:cret+%", client+" "+secret)
			assert.Equal(t, "urn:ietf:params:oauth:grant-type:token-exchange", r.Form.Get("grant_type"))
			assert.Equal(t, "urn:ietf:params:oauth:token-type:access_token", r.Form.Get("subject_token_type"))
			assert.Equal(t, "urn:ietf:params:oauth:token-type:jwt", r.Form.Get("requested_token_type"))
			assert.Equal(t, []string{"service"}, r.Form["audience"])
			// The issuer's clock is a little ahead, as it can be on a real installation.
			claims := jwt.Claims{
				Issuer: issuer.URL, Subject: "user", Audience: jwt.Audience{"service"},
				IssuedAt: jwt.NewNumericDate(time.Now().Add(5 * time.Second)),
				Expiry:   jwt.NewNumericDate(time.Now().Add(time.Hour)),
			}
			response, sign, org := map[string]any{
				"token_type": "Bearer", "expires_in": 3600,
				"issued_token_type": "urn:ietf:params:oauth:token-type:jwt",
			}, signer, "org"
			switch r.Form.Get("subject_token") {
			case "other-user":
				claims.Subject = "mallory"
			case "opens-loom":
				claims.Audience = jwt.Audience{"service", "harness"}
			case "missing-audience":
				claims.Audience = jwt.Audience{"another-service"}
			case "other-issuer":
				claims.Issuer = "https://other.example"
			case "expired":
				claims.Expiry = jwt.NewNumericDate(time.Now().Add(-time.Second))
			case "forged":
				sign = forger
			case "other-org":
				org = "other"
			case "wrong-type":
				response["token_type"] = "N_A"
			case "zero-lifetime":
				response["expires_in"] = 0
			case "other-issued-type":
				response["issued_token_type"] = "urn:ietf:params:oauth:token-type:access_token"
			case "no-lifetime":
				delete(response, "expires_in")
			}
			token, signErr := jwt.Signed(sign).Claims(claims).
				Claims(map[string]any{"urn:zitadel:iam:user:resourceowner:id": org}).Serialize()
			assert.NoError(t, signErr)
			response["access_token"] = token
			_ = json.NewEncoder(w).Encode(response)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer issuer.Close()
	auth, err := identity.New(config.Identity{
		Issuer: issuer.URL, Audience: "mcp", ClientID: "client", ClientSecret: "secret", JWKSCacheTTLSeconds: 60,
	}, issuer.Client(), nil)
	require.NoError(t, err)
	tokens := identity.NewServiceTokens(auth, identity.ServiceTokenExchange{
		ClientID: "exchange@project", ClientSecret: "se:cret+%",
		Audiences: []string{"service"}, Forbidden: []string{"harness", "mcp"},
	})
	caller := func(token string) domain.ExternalCaller {
		return domain.ExternalCaller{
			Principal: domain.Principal{
				Issuer: issuer.URL, Subject: "user", OrgID: "org", ExpiresAt: time.Now().Add(time.Hour),
			},
			Token: token,
		}
	}
	exchange := func(held domain.ExternalCaller) int32 {
		before := exchanges.Load()
		_, exchangeErr := tokens.For(context.Background(), held)
		require.NoError(t, exchangeErr, held.Token)
		return exchanges.Load() - before
	}

	service, err := tokens.For(context.Background(), caller("agent"))
	require.NoError(t, err)
	require.NotEmpty(t, service)
	require.NotEqual(t, "agent", service, "the agent's token must not reach a service")
	again, err := tokens.For(context.Background(), caller("agent"))
	require.NoError(t, err)
	require.Equal(t, service, again)
	require.EqualValues(t, 1, exchanges.Load(), "a valid service token is reused")

	for _, token := range []string{
		"other-user", "other-org", "opens-loom", "missing-audience", "other-issuer", "expired", "forged",
		"wrong-type", "zero-lifetime", "other-issued-type",
	} {
		_, err = tokens.For(context.Background(), caller(token))
		require.ErrorIs(t, err, identity.ErrUnauthorized, token)
	}

	// The response lifetime is optional; the signed expiry then bounds the reuse.
	require.EqualValues(t, 1, exchange(caller("no-lifetime")))
	require.EqualValues(t, 0, exchange(caller("no-lifetime")))
	// After a role change of the same agent token, the service gets a token with the new roles.
	granted := caller("agent")
	granted.Principal.Permissions = map[string]bool{"notes.read": true}
	require.EqualValues(t, 1, exchange(granted))
	require.EqualValues(t, 0, exchange(granted))
	// A service token is never reused beyond the agent's own token.
	closing := caller("closing")
	closing.Principal.ExpiresAt = time.Now().Add(10 * time.Second)
	require.EqualValues(t, 1, exchange(closing))
	require.EqualValues(t, 1, exchange(closing))
}
