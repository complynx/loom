package identity_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/identity"
)

type cachedIAM struct {
	server         *httptest.Server
	calls          atomic.Int32
	down           atomic.Bool
	revoked        atomic.Bool
	bindingRemoved atomic.Bool
	expires        atomic.Int64
	epoch          atomic.Int64
	block          chan struct{}
	entered        chan struct{}
}

func newCachedIAM(t *testing.T) *cachedIAM {
	t.Helper()
	f := &cachedIAM{}
	f.expires.Store(time.Now().Add(time.Hour).Unix())
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/openid-configuration" {
			_ = json.NewEncoder(w).
				Encode(map[string]string{"issuer": f.server.URL, "introspection_endpoint": f.server.URL + "/introspect", "jwks_uri": f.server.URL + "/keys"})
			return
		}
		f.calls.Add(1)
		if f.block != nil {
			select {
			case f.entered <- struct{}{}:
			default:
			}
			select {
			case <-f.block:
			case <-r.Context().Done():
				return
			}
		}
		if f.down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		subject := "user"
		if r.ParseForm() == nil && r.Form.Get("token") == "service" {
			subject = "svc"
		}
		_ = json.NewEncoder(w).
			Encode(map[string]any{"active": !f.revoked.Load(), "iss": f.server.URL, "sub": subject, "aud": "harness", "exp": f.expires.Load(), "urn:zitadel:iam:user:resourceowner:id": "org", "urn:zitadel:iam:org:project:roles": map[string]any{"member": map[string]string{"org": "example"}}})
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *cachedIAM) auth(t *testing.T, ttl int) *identity.Authenticator {
	t.Helper()
	a, err := identity.New(
		config.Identity{
			Issuer:                     f.server.URL,
			Audience:                   "harness",
			ClientID:                   "client",
			ClientSecret:               "secret",
			JWKSCacheTTLSeconds:        60,
			PermissionsCacheTTLSeconds: ttl,
			ServiceSubjects:            []string{"svc"},
			RolePermissions:            map[string][]string{"member": {"chat.read", "settings.write"}},
		},
		f.server.Client(),
		func(context.Context, *http.Request, domain.Principal) (identity.Binding, error) {
			if f.bindingRemoved.Load() {
				return identity.Binding{}, identity.ErrUnauthorized
			}
			return identity.Binding{Actor: "svc", Permissions: map[string]bool{"chat.read": true}}, nil
		},
	)
	require.NoError(t, err)
	a.UseCacheControl(
		func(context.Context) (int64, error) { return f.epoch.Load(), nil },
		func(context.Context) (int64, error) { return f.epoch.Add(1), nil },
		nil,
	)
	return a
}

func cachedAuthenticate(ctx context.Context, auth *identity.Authenticator, token string) (domain.Principal, error) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	return auth.Authenticate(ctx, r)
}

func TestIdentityCacheOutageFreshRevocationAndPermissionIsolation(t *testing.T) {
	t.Parallel()
	f := newCachedIAM(t)
	a := f.auth(t, 120)
	ctx := context.Background()
	p, err := cachedAuthenticate(ctx, a, "token")
	require.NoError(t, err)
	require.True(t, p.Has("chat.read"))
	p.Permissions["injected"] = true
	f.down.Store(true)
	p, err = cachedAuthenticate(ctx, a, "token")
	require.NoError(t, err)
	require.False(t, p.Has("injected"))
	require.EqualValues(t, 1, f.calls.Load())
	_, err = cachedAuthenticate(ctx, a, "other")
	require.ErrorIs(t, err, identity.ErrUnavailable)
	_, err = cachedAuthenticate(identity.RequireFresh(ctx), a, "token")
	require.ErrorIs(t, err, identity.ErrUnavailable)
	_, err = cachedAuthenticate(ctx, a, "token")
	require.ErrorIs(t, err, identity.ErrUnavailable)
	f.down.Store(false)
	_, err = cachedAuthenticate(ctx, a, "token")
	require.NoError(t, err)
	f.revoked.Store(true)
	_, err = cachedAuthenticate(identity.RequireFresh(ctx), a, "token")
	require.ErrorIs(t, err, identity.ErrUnauthorized)
	_, err = cachedAuthenticate(ctx, a, "token")
	require.ErrorIs(t, err, identity.ErrUnauthorized)
	f.revoked.Store(false)
	_, err = cachedAuthenticate(ctx, a, "token")
	require.NoError(t, err, "negative authority must not be cached")
}

func TestIdentityCachePurgeInvalidatesAllReplicas(t *testing.T) {
	t.Parallel()
	f := newCachedIAM(t)
	a, b := f.auth(t, 120), f.auth(t, 120)
	ctx := context.Background()
	_, err := cachedAuthenticate(ctx, a, "token")
	require.NoError(t, err)
	_, err = cachedAuthenticate(ctx, b, "token")
	require.NoError(t, err)
	f.revoked.Store(true)
	epoch, err := a.ClearPermissionsCache(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, epoch)
	for _, auth := range []*identity.Authenticator{a, b} {
		_, err = cachedAuthenticate(ctx, auth, "token")
		require.ErrorIs(t, err, identity.ErrUnauthorized)
	}
}

func TestIdentityCachePurgeRejectsInflightResult(t *testing.T) {
	t.Parallel()
	f := newCachedIAM(t)
	f.block, f.entered = make(chan struct{}), make(chan struct{}, 1)
	a, b := f.auth(t, 120), f.auth(t, 120)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := cachedAuthenticate(ctx, b, "token"); done <- err }()
	select {
	case <-f.entered:
	case <-ctx.Done():
		t.Fatal("introspection did not start")
	}
	_, err := a.ClearPermissionsCache(ctx)
	require.NoError(t, err)
	close(f.block)
	require.ErrorIs(t, <-done, identity.ErrUnavailable)
	f.revoked.Store(true)
	_, err = cachedAuthenticate(ctx, b, "token")
	require.ErrorIs(t, err, identity.ErrUnauthorized)
}

func TestIdentityCacheExpiryAndDisabled(t *testing.T) {
	t.Parallel()
	f := newCachedIAM(t)
	a := f.auth(t, 1)
	_, err := cachedAuthenticate(context.Background(), a, "token")
	require.NoError(t, err)
	f.down.Store(true)
	time.Sleep(1100 * time.Millisecond)
	_, err = cachedAuthenticate(context.Background(), a, "token")
	require.ErrorIs(t, err, identity.ErrUnavailable)
	f.down.Store(false)
	b := f.auth(t, 0)
	_, err = cachedAuthenticate(context.Background(), b, "token")
	require.NoError(t, err)
	f.revoked.Store(true)
	_, err = cachedAuthenticate(context.Background(), b, "token")
	require.ErrorIs(t, err, identity.ErrUnauthorized)
}

func TestIdentityCacheNeverExtendsTokenExpiry(t *testing.T) {
	t.Parallel()
	f := newCachedIAM(t)
	f.expires.Store(time.Now().Add(2 * time.Second).Unix())
	a := f.auth(t, 120)
	_, err := cachedAuthenticate(context.Background(), a, "token")
	require.NoError(t, err)
	f.down.Store(true)
	time.Sleep(time.Until(time.Unix(f.expires.Load(), 0)) + 10*time.Millisecond)
	_, err = cachedAuthenticate(context.Background(), a, "token")
	require.Error(t, err, "expired cached credentials must never authorize offline")
}

func TestIdentityCacheCoalescesAndSurvivesCancelledWaiter(t *testing.T) {
	t.Parallel()
	f := newCachedIAM(t)
	f.block, f.entered = make(chan struct{}), make(chan struct{}, 1)
	a := f.auth(t, 120)
	joined := make(chan struct{}, 1)
	a.UseCacheControl(nil, nil, func(_ context.Context, event string, _ time.Duration) {
		if event == "joined" {
			select {
			case joined <- struct{}{}:
			default:
			}
		}
	})
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	leader, cancel := context.WithCancel(ctx)
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { _, err := cachedAuthenticate(leader, a, "token"); first <- err }()
	select {
	case <-f.entered:
	case <-ctx.Done():
		t.Fatal("introspection did not start")
	}
	go func() { _, err := cachedAuthenticate(ctx, a, "token"); second <- err }()
	select {
	case <-joined:
	case <-ctx.Done():
		t.Fatal("second request did not join")
	}
	cancel()
	require.ErrorIs(t, <-first, context.Canceled)
	close(f.block)
	require.NoError(t, <-second)
	require.EqualValues(t, 1, f.calls.Load())
}

func TestIdentityCacheRequiresAvailableInvalidationAuthority(t *testing.T) {
	t.Parallel()
	f := newCachedIAM(t)
	a := f.auth(t, 120)
	var unavailable atomic.Bool
	a.UseCacheControl(func(context.Context) (int64, error) {
		if unavailable.Load() {
			return 0, identity.ErrUnavailable
		}
		return 0, nil
	}, nil, nil)
	_, err := cachedAuthenticate(context.Background(), a, "token")
	require.NoError(t, err)
	unavailable.Store(true)
	_, err = cachedAuthenticate(context.Background(), a, "token")
	require.ErrorIs(t, err, identity.ErrUnavailable)
	require.EqualValues(t, 1, f.calls.Load())
}

func TestIdentityCacheDelegationRechecksBindingAndBothTokens(t *testing.T) {
	t.Parallel()
	f := newCachedIAM(t)
	a := f.auth(t, 120)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer service")
	r.Header.Set("X-Subject-Token", "user-token")
	p, err := a.Authenticate(context.Background(), r)
	require.NoError(t, err)
	require.Equal(t, "svc", p.Actor)
	require.True(t, p.Has("chat.read"))
	require.False(t, p.Has("settings.write"))
	require.EqualValues(t, 2, f.calls.Load())
	f.down.Store(true)
	_, err = a.Authenticate(context.Background(), r)
	require.NoError(t, err)
	require.EqualValues(t, 2, f.calls.Load())
	f.bindingRemoved.Store(true)
	_, err = a.Authenticate(context.Background(), r)
	require.ErrorIs(t, err, identity.ErrUnauthorized)
	f.bindingRemoved.Store(false)
	r.Header.Set("X-Subject-Token", "different-token")
	_, err = a.Authenticate(context.Background(), r)
	require.ErrorIs(t, err, identity.ErrUnavailable)
}

func TestIdentityCachePurgeDoesNotBypassFillLimit(t *testing.T) {
	t.Parallel()
	f := newCachedIAM(t)
	f.block, f.entered = make(chan struct{}), make(chan struct{}, 1)
	a := f.auth(t, 120)
	const capacity = 64
	started := make(chan struct{}, capacity)
	a.UseCacheControl(nil, nil, func(_ context.Context, event string, _ time.Duration) {
		if event == "miss" {
			started <- struct{}{}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, capacity)
	for i := range capacity {
		go func() { _, err := cachedAuthenticate(ctx, a, "token-"+strconv.Itoa(i)); done <- err }()
	}
	for range capacity {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("fills did not start")
		}
	}
	_, err := a.ClearPermissionsCache(ctx)
	require.NoError(t, err)
	_, err = cachedAuthenticate(ctx, a, "new-token")
	require.ErrorIs(t, err, identity.ErrUnavailable, "invalidated fills must still occupy capacity")
	close(f.block)
	for range capacity {
		require.ErrorIs(t, <-done, identity.ErrUnavailable)
	}
	_, err = cachedAuthenticate(ctx, a, "new-token")
	require.NoError(t, err, "completed fills must release capacity")
}
