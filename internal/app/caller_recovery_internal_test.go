package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/api"
	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/identity"
	"github.com/complynx/loom/internal/modules"
	"github.com/complynx/loom/internal/store"
	"github.com/complynx/loom/internal/telemetry"
)

type callerRecoveryFixture struct {
	present         atomic.Bool
	actorRevoked    atomic.Bool
	userRevoked     atomic.Bool
	restricted      atomic.Bool
	userRoleRemoved atomic.Bool
	duplicate       atomic.Bool
	issuer          string
	registry        *modules.Registry
	handler         http.Handler
	authorize       RunAuthorizer
	repository      *store.Store
	principal       domain.Principal
	session         domain.AuthSession
	run             domain.Run
	endpoint        modules.Endpoint
}

func (f *callerRecoveryFixture) serveIdentity(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		_ = json.NewEncoder(w).
			Encode(map[string]string{"issuer": f.issuer, "jwks_uri": f.issuer + "/keys", "introspection_endpoint": f.issuer + "/introspect"})
	case "/introspect":
		_ = r.ParseForm()
		token := r.Form.Get("token")
		active := (token == "actor" && !f.actorRevoked.Load()) || (token == "user" && !f.userRevoked.Load())
		roles := map[string]any{"member": map[string]string{"org": "example"}}
		if token == "user" && f.userRoleRemoved.Load() {
			roles = map[string]any{}
		}
		_ = json.NewEncoder(w).
			Encode(map[string]any{"active": active, "iss": f.issuer, "sub": token, "aud": "harness", "exp": time.Now().Add(time.Hour).Unix(), "urn:zitadel:iam:user:resourceowner:id": "org", "urn:zitadel:iam:org:project:roles": roles})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *callerRecoveryFixture) manifest() modules.Manifest {
	permissions := []string{"runs.create", "runs.control", "chat.read"}
	if f.restricted.Load() {
		permissions = []string{"runs.create"}
	}
	return modules.Manifest{
		ProtocolVersion: 1,
		ServiceID:       "shuttle",
		Revision:        "1",
		Description:     "Recovery fixture",
		Modules:         []modules.Module{},
		Caller: &modules.Caller{
			Permissions: permissions,
			Auth:        modules.CallerAuth{Type: "zitadel", Issuer: f.issuer, Subject: "actor", Audience: "harness"},
		},
	}
}

func newCallerRecoveryFixture(t *testing.T, acceptedContext ...context.Context) *callerRecoveryFixture {
	t.Helper()
	f := &callerRecoveryFixture{}
	f.present.Store(true)
	issuer := httptest.NewServer(http.HandlerFunc(f.serveIdentity))
	t.Cleanup(issuer.Close)
	f.issuer = issuer.URL
	repository, _ := searchToolStore(t)
	f.repository = repository
	cfg := config.Identity{
		Issuer:          f.issuer,
		Audience:        "harness",
		ClientID:        "client",
		ClientSecret:    "test-only",
		ServiceSubjects: []string{"actor"},
		RolePermissions: map[string][]string{"member": {"runs.create", "runs.control", "chat.read", "shuttle.read"}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := f.manifest()
		if r.URL.Path == "/second" {
			m.ServiceID = "second"
		}
		_ = json.NewEncoder(w).Encode(m)
	}))
	t.Cleanup(server.Close)
	address, err := url.Parse(server.URL)
	require.NoError(t, err)
	f.endpoint = modules.Endpoint{
		ContainerID: "shuttle",
		URI:         "http://registered:" + address.Port() + "/manifest",
		Address:     "127.0.0.1",
	}
	source := modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		if !f.present.Load() {
			return nil, nil
		}
		endpoints := []modules.Endpoint{
			{ContainerID: "shuttle", URI: "http://registered:" + address.Port() + "/manifest", Address: "127.0.0.1"},
		}
		if f.duplicate.Load() {
			endpoints = append(
				endpoints,
				modules.Endpoint{
					ContainerID: "second",
					URI:         "http://registered:" + address.Port() + "/second",
					Address:     "127.0.0.1",
				},
			)
		}
		return endpoints, nil
	})
	f.registry, err = modules.NewRegistry(
		source,
		modules.Options{OnCallerBindings: rememberCallerBindings(cfg, repository)},
	)
	require.NoError(t, err)
	require.NoError(t, f.registry.Refresh(t.Context()))
	cipher, err := identity.NewSessionCipher("test", make([]byte, 32))
	require.NoError(t, err)
	check := func(ctx context.Context, p domain.Principal) (identity.Binding, error) {
		return callerBinding(ctx, f.registry, cfg, p)
	}
	auth, err := runtimeAuthenticator(config.Config{Identity: cfg}, repository, f.registry, cipher, check, &sync.Map{})
	require.NoError(t, err)
	initial := httptest.NewRequest(http.MethodGet, "http://loom/v1/runs", nil)
	initial.Header.Set("Authorization", "Bearer actor")
	initial.Header.Set("X-Subject-Token", "user")
	f.principal, err = auth.Authenticate(t.Context(), initial)
	require.NoError(t, err)
	envelope, err := cipher.SealRequest(t.Context(), initial, f.principal)
	require.NoError(t, err)
	f.session, err = repository.CreateAuthSession(t.Context(), f.principal, envelope, f.principal.ExpiresAt)
	require.NoError(t, err)
	ctx := t.Context()
	if len(acceptedContext) > 0 {
		ctx = acceptedContext[0]
	}
	f.run, err = repository.CreateRun(
		ctx,
		f.principal,
		"caller-recovery",
		domain.CreateRun{AuthSessionID: f.session.ID, Input: domain.Input{Text: "pending inference"}},
	)
	require.NoError(t, err)
	f.run, err = repository.ClaimRun(t.Context(), f.run.ID, "worker", time.Minute)
	require.NoError(t, err)
	f.handler = api.New(repository, auth, api.Options{})
	f.authorize = executionAuthorizer(repository, auth, cipher, check, telemetry.New(slog.New(slog.DiscardHandler)))
	return f
}

func (f *callerRecoveryFixture) query() *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "http://loom/v1/runs/"+f.run.ID, nil)
	request.Header.Set("Authorization", "Bearer actor")
	request.Header.Set("X-Auth-Session-Id", f.session.ID)
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	return response
}

func TestDelegatedCallerRestartHasRetryableErrorAndSameRunRecovery(t *testing.T) {
	t.Parallel()
	f := newCallerRecoveryFixture(t)
	require.Equal(t, http.StatusOK, f.query().Code)
	f.present.Store(false)
	require.NoError(t, f.registry.Refresh(t.Context()))
	unavailable := f.query()
	require.Equal(t, http.StatusServiceUnavailable, unavailable.Code)
	require.Contains(t, unavailable.Body.String(), `"code":"caller_binding_unavailable"`)
	require.Contains(t, unavailable.Body.String(), `"retryable":true`)
	require.Empty(t, unavailable.Header().Get("WWW-Authenticate"))
	rawRequest := httptest.NewRequest(http.MethodGet, "http://loom/v1/runs/"+f.run.ID, nil)
	rawRequest.Header.Set("Authorization", "Bearer actor")
	rawRequest.Header.Set("X-Subject-Token", "user")
	rawResponse := httptest.NewRecorder()
	f.handler.ServeHTTP(rawResponse, rawRequest)
	require.Equal(t, http.StatusServiceUnavailable, rawResponse.Code)
	require.Contains(t, rawResponse.Body.String(), `"code":"caller_binding_unavailable"`)
	p, _, err := f.authorize(t.Context(), f.run)
	require.Error(t, err)
	require.Empty(t, p.Subject)
	f.userRevoked.Store(true)
	require.Equal(t, http.StatusUnauthorized, f.query().Code)
	f.userRevoked.Store(false)
	f.present.Store(true)
	require.NoError(t, f.registry.Refresh(t.Context()))
	recovered := f.query()
	require.Equal(t, http.StatusOK, recovered.Code)
	require.Contains(t, recovered.Body.String(), f.run.ID)
	_, _, err = f.authorize(t.Context(), f.run)
	require.NoError(t, err)
	retained, err := f.repository.GetRun(t.Context(), f.principal, f.run.ID)
	require.NoError(t, err)
	require.Equal(t, domain.Running, retained.State)
	require.Zero(t, retained.SpentMicrocredits)
	require.Equal(t, f.session.ID, retained.AuthSessionID)
	checkCallerRevocations(t, f)
}

func checkCallerRevocations(t *testing.T, f *callerRecoveryFixture) {
	t.Helper()
	f.restricted.Store(true)
	require.NoError(t, f.registry.Refresh(t.Context()))
	require.Equal(t, http.StatusForbidden, f.query().Code)
	f.restricted.Store(false)
	require.NoError(t, f.registry.Refresh(t.Context()))
	f.userRoleRemoved.Store(true)
	require.Equal(t, http.StatusOK, f.query().Code)
	fresh, _, roleErr := f.authorize(t.Context(), f.run)
	require.NoError(t, roleErr)
	require.False(t, fresh.Has("shuttle.read"), "user module roles are not supplied by the caller")
	f.userRoleRemoved.Store(false)
	f.userRevoked.Store(true)
	require.Equal(t, http.StatusUnauthorized, f.query().Code)
	f.userRevoked.Store(false)
	f.actorRevoked.Store(true)
	f.present.Store(false)
	require.NoError(t, f.registry.Refresh(t.Context()))
	require.Equal(t, http.StatusUnauthorized, f.query().Code)
	f.actorRevoked.Store(false)
	f.present.Store(true)
	f.duplicate.Store(true)
	require.NoError(t, f.registry.Refresh(t.Context()))
	require.Equal(t, http.StatusUnauthorized, f.query().Code)
	f.duplicate.Store(false)
	require.NoError(t, f.registry.Refresh(t.Context()))
	require.NoError(t, f.repository.RevokeAuthSession(t.Context(), f.principal, f.session.ID))
	assert.NotEqual(t, http.StatusOK, f.query().Code)
	assert.NotEqual(t, http.StatusServiceUnavailable, f.query().Code)
	f.present.Store(false)
	require.NoError(t, f.registry.Refresh(t.Context()))
	assert.NotEqual(t, http.StatusOK, f.query().Code)
	assert.NotEqual(t, http.StatusServiceUnavailable, f.query().Code)
}
