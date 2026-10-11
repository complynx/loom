package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/api"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/identity"
)

type authFunc func(context.Context, *http.Request) (domain.Principal, error)

func (f authFunc) Authenticate(ctx context.Context, r *http.Request) (domain.Principal, error) {
	return f(ctx, r)
}
func allowed(_ context.Context, _ *http.Request) (domain.Principal, error) {
	return domain.Principal{
		Issuer:      "https://issuer.example",
		Subject:     "alice",
		Actor:       "alice",
		OrgID:       "org-a",
		Permissions: map[string]bool{"chat.read": true, "runs.create": true, "runs.control": true},
		ExpiresAt:   time.Now().Add(time.Hour),
	}, nil
}
func request(handler http.Handler, method, path, body, contentType, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", contentType)
	r.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}
func TestAuthorizationFailsClosed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		auth   api.Authenticator
		status int
		code   string
	}{
		{"missing configuration", nil, 503, "dependency_unavailable"},
		{"invalid credentials", authFunc(func(context.Context, *http.Request) (domain.Principal, error) {
			return domain.Principal{}, identity.ErrUnauthorized
		}), 401, "unauthorized"},
		{"issuer unavailable", authFunc(func(context.Context, *http.Request) (domain.Principal, error) {
			return domain.Principal{}, identity.ErrUnavailable
		}), 503, "dependency_unavailable"},
		{"missing permission", authFunc(func(context.Context, *http.Request) (domain.Principal, error) {
			return domain.Principal{Subject: "alice", OrgID: "org-a"}, nil
		}), 403, "forbidden"},
		{"expired authorization", authFunc(func(ctx context.Context, _ *http.Request) (domain.Principal, error) {
			p, _ := allowed(ctx, nil)
			p.ExpiresAt = time.Now().Add(-time.Second)
			return p, nil
		}), 401, "unauthorized"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := request(api.New(nil, tc.auth, api.Options{}), "GET", "/v1/chats/default", "", "", "")
			require.Equal(t, tc.status, w.Code)
			require.Contains(t, w.Body.String(), tc.code)
			require.NotContains(t, w.Body.String(), "token")
		})
	}
}
func TestStrictRunRequestContract(t *testing.T) {
	t.Parallel()
	handler := api.New(nil, authFunc(allowed), api.Options{MaxBodyBytes: 256})
	cases := []struct {
		name, body, mime, key string
		status                int
		code                  string
	}{
		{"unknown field", `{"model":"bypass"}`, "application/json", "k", 422, "invalid_request"},
		{
			"nested unknown field",
			`{"input":{"text":"hello","admin":true}}`,
			"application/json",
			"k",
			422,
			"invalid_request",
		},
		{"multiple objects", "{} {}", "application/json", "k", 422, "invalid_request"},
		{"null", "null", "application/json", "k", 422, "invalid_request"},
		{"wrong MIME", "{}", "application/jsonp", "k", 415, "unsupported_media_type"},
		{"missing key", "{}", "application/json", "", 422, "invalid_idempotency_key"},
		{
			"over limit",
			`{"input":{"text":"` + strings.Repeat("x", 300) + `"}}`,
			"application/json",
			"k",
			413,
			"body_too_large",
		},
		{
			"unbounded wait",
			`{"delivery":{"mode":"wait","wait_ms":31000}}`,
			"application/json",
			"k",
			422,
			"invalid_delivery",
		},
		{"unsupported delivery", `{"delivery":{"mode":"forever"}}`, "application/json", "k", 422, "invalid_delivery"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := request(handler, "POST", "/v1/runs", tc.body, tc.mime, tc.key)
			require.Equal(t, tc.status, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), tc.code)
		})
	}
}
func TestSSERejectsAmbiguousCursor(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodGet, "/v1/runs/ignored/events?after=1", nil)
	r.Header.Set("Last-Event-ID", "2")
	w := httptest.NewRecorder()
	api.New(nil, authFunc(allowed), api.Options{}).ServeHTTP(w, r)
	require.Equal(t, 422, w.Code)
	require.Contains(t, w.Body.String(), "invalid_cursor")
}
func TestReadinessAndLiveness(t *testing.T) {
	t.Parallel()
	handler := api.New(
		nil,
		nil,
		api.Options{Ready: func(context.Context) error { return errors.New("password-secret") }},
	)
	w := request(handler, "GET", "/healthz", "", "", "")
	require.Equal(t, 200, w.Code)
	w = request(handler, "GET", "/readyz", "", "", "")
	require.Equal(t, 503, w.Code)
	require.NotContains(t, w.Body.String(), "password-secret")
}

func TestClientCannotAssignRunInputState(t *testing.T) {
	t.Parallel()
	handler := api.New(nil, authFunc(allowed), api.Options{})
	for _, body := range []string{`{"kind":"steering","text":"hello","id":""}`, `{"kind":"steering","text":"hello","applied":false}`} {
		w := request(handler, "POST", "/v1/runs/ignored/input", body, "application/json", "k")
		require.Equal(t, 422, w.Code)
		require.Contains(t, w.Body.String(), "invalid_request")
	}
}
func TestOpenAPIExposesImplementedSurface(t *testing.T) {
	t.Parallel()
	w := request(api.New(nil, authFunc(allowed), api.Options{}), "GET", "/openapi.json", "", "", "")
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), "/v1/runs")
	require.Contains(t, w.Body.String(), "/v1/artifacts")
	require.Contains(t, w.Body.String(), "x-required-permission")
}
