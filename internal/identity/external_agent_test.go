package identity_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/identity"
)

func TestExternalAuthenticationKeepsOnlyModulePermissions(t *testing.T) {
	t.Parallel()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	var issuer string
	server := authenticationServer(t, key, &issuer)
	defer server.Close()
	issuer = server.URL
	auth, err := identity.New(config.Identity{
		Issuer:              issuer,
		Audience:            "harness",
		ClientID:            "client",
		ClientSecret:        "secret",
		JWKSCacheTTLSeconds: 60,
		ServiceSubjects:     []string{"svc"},
		RolePermissions: map[string][]string{
			"member": {"chat.read", "runs.create", "external_agent.profile.write", "massage.read"},
		},
	}, server.Client(), nil)
	require.NoError(t, err)
	request := func(token string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "http://harness/mcp", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		return r
	}

	caller, err := auth.AuthenticateExternal(context.Background(), request("opaque"))
	require.NoError(t, err)
	require.Equal(t, "user", caller.Principal.Subject)
	require.Equal(t, "agent-client", caller.ClientID)
	require.Equal(t, "opaque", caller.Token)
	require.Equal(t, map[string]bool{"massage.read": true}, caller.Principal.Permissions)

	for _, token := range []string{"service", "revoked", "wrong-audience"} {
		_, err = auth.AuthenticateExternal(context.Background(), request(token))
		require.ErrorIs(t, err, identity.ErrUnauthorized, token)
	}
	delegated := request("opaque")
	delegated.Header.Set("X-Subject-Token", "opaque")
	_, err = auth.AuthenticateExternal(context.Background(), delegated)
	require.ErrorIs(t, err, identity.ErrUnauthorized)
}
