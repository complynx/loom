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
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/identity"
)

// The right to write a profile must come from the confirming caller's binding.
// A role of the user must not grant it in a delegated request.
func TestProfilePermissionComesFromCallerBinding(t *testing.T) {
	t.Parallel()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	var issuer string
	server := authenticationServer(t, key, &issuer)
	defer server.Close()
	issuer = server.URL
	bound := map[string]bool{"chat.read": true}
	auth, err := identity.New(config.Identity{
		Issuer:              issuer,
		Audience:            "harness",
		ClientID:            "client",
		ClientSecret:        "secret",
		JWKSCacheTTLSeconds: 60,
		ServiceSubjects:     []string{"svc"},
		RolePermissions:     map[string][]string{"member": {"chat.read", domain.ExternalAgentProfilePermission}},
	}, server.Client(), func(context.Context, *http.Request, domain.Principal) (identity.Binding, error) {
		return identity.Binding{Actor: "svc", Permissions: bound}, nil
	})
	require.NoError(t, err)
	delegated := httptest.NewRequest(http.MethodPut, "http://harness/v1/users/me/external-agent", nil)
	delegated.Header.Set("Authorization", "Bearer service")
	delegated.Header.Set("X-Subject-Token", "opaque")

	p, err := auth.Authenticate(context.Background(), delegated)
	require.NoError(t, err)
	require.False(t, p.Has(domain.ExternalAgentProfilePermission))
	bound = map[string]bool{domain.ExternalAgentProfilePermission: true}
	p, err = auth.Authenticate(context.Background(), delegated)
	require.NoError(t, err)
	require.True(t, p.Has(domain.ExternalAgentProfilePermission))
	require.Equal(t, "user", p.Subject)
	require.Equal(t, "svc", p.Actor)
}

// A token for one endpoint must not open the other, also when it names both audiences.
func TestEndpointAudiencesAreMutuallyExclusive(t *testing.T) {
	t.Parallel()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	var issuer string
	server := authenticationServer(t, key, &issuer)
	defer server.Close()
	issuer = server.URL
	authenticator := func(audience string, reject ...string) *identity.Authenticator {
		auth, newErr := identity.New(config.Identity{
			Issuer: issuer, Audience: audience, RejectAudiences: reject,
			ClientID: "client", ClientSecret: "secret", JWKSCacheTTLSeconds: 60,
		}, server.Client(), nil)
		require.NoError(t, newErr)
		return auth
	}
	request := func(token string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "http://harness/", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		return r
	}
	// The MCP endpoint also refuses the audiences of downstream services.
	api, mcp := authenticator("harness", "mcp"), authenticator("mcp", "service", "harness")

	_, err = api.Authenticate(context.Background(), request("opaque"))
	require.NoError(t, err)
	_, err = mcp.AuthenticateExternal(context.Background(), request("mcp-only"))
	require.NoError(t, err)
	for _, token := range []string{"mcp-only", "both-audiences"} {
		_, err = api.Authenticate(context.Background(), request(token))
		require.ErrorIs(t, err, identity.ErrUnauthorized, token)
	}
	for _, token := range []string{"opaque", "both-audiences", "mcp-and-service"} {
		_, err = mcp.AuthenticateExternal(context.Background(), request(token))
		require.ErrorIs(t, err, identity.ErrUnauthorized, token)
	}
}

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
