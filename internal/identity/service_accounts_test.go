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

func TestServiceAccountSelfAuthenticationAndSessionRevocation(t *testing.T) {
	t.Parallel()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	var issuer string
	server := authenticationServer(t, key, &issuer)
	t.Cleanup(server.Close)
	issuer = server.URL
	cfg := config.Identity{
		Issuer:       issuer,
		Audience:     "harness",
		ClientID:     "client",
		ClientSecret: "secret",
		ServiceSubjects: []string{
			"svc",
		},
		ServiceAccounts: map[string][]string{"svc": {"runs.create", "chat.read", "massage.read", "not-in-role"}},
		RolePermissions: map[string][]string{"member": {"runs.create", "chat.read", "massage.read", "settings.write"}},
	}
	auth, err := identity.New(cfg, server.Client(), nil)
	require.NoError(t, err)
	var classifierCalls int
	require.NoError(t, auth.UseServiceClassifier(func(context.Context, domain.Principal) (bool, error) {
		classifierCalls++
		return false, identity.ErrUnavailable
	}))
	request := httptest.NewRequest(http.MethodPost, "http://loom/v1/runs", nil)
	request.Header.Set("Authorization", "Bearer service")
	p, err := auth.Authenticate(t.Context(), request)
	require.NoError(t, err)
	require.True(t, p.ServiceAccount)
	require.Equal(t, p.Subject, p.Actor)
	require.True(t, p.Has("massage.read"))
	require.False(t, p.Has("settings.write"))
	require.False(t, p.Has("not-in-role"))
	cipher, err := identity.NewSessionCipher("key", make([]byte, 32))
	require.NoError(t, err)
	sealed, err := cipher.SealRequest(t.Context(), request, p)
	require.NoError(t, err)
	fresh, token, err := auth.VerifySession(t.Context(), cipher, p, sealed, nil)
	require.NoError(t, err)
	require.True(t, fresh.ServiceAccount)
	require.Equal(t, "service", token)
	require.Zero(t, classifierCalls, "self-authentication must not depend on discovered business services")
	request.Header.Set("X-Subject-Token", "opaque")
	_, err = auth.Authenticate(t.Context(), request)
	require.ErrorIs(t, err, identity.ErrUnauthorized)
	request.Header.Del("X-Subject-Token")
	cfg.ServiceAccounts = map[string][]string{"svc": {"chat.read"}}
	reduced, err := identity.New(cfg, server.Client(), nil)
	require.NoError(t, err)
	fresh, _, err = reduced.VerifySession(t.Context(), cipher, p, sealed, nil)
	require.NoError(t, err)
	require.False(t, fresh.Has("runs.create"))
	cfg.ServiceAccounts = nil
	revoked, err := identity.New(cfg, server.Client(), nil)
	require.NoError(t, err)
	_, err = revoked.Authenticate(t.Context(), request)
	require.ErrorIs(t, err, identity.ErrUnauthorized)
	_, _, err = revoked.VerifySession(t.Context(), cipher, p, sealed, nil)
	require.ErrorIs(t, err, identity.ErrUnauthorized)
}

func TestServiceAccountRequiresPersistentMachineClassification(t *testing.T) {
	t.Parallel()
	err := (config.Identity{ServiceAccounts: map[string][]string{"svc": {"chat.read"}}}).ValidateServiceAccounts()
	require.ErrorContains(t, err, "service_subjects")
}
