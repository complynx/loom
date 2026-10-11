package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/clx-scripter/internal/config"
	"github.com/complynx/clx-scripter/internal/domain"
)

type operatorTestAuth struct {
	denied bool
	calls  int
}

func (a *operatorTestAuth) Authenticate(_ context.Context, r *http.Request) (domain.Principal, error) {
	a.calls++
	if r.Header.Get("Authorization") != "Bearer stub-access" {
		return domain.Principal{}, errors.New("bad token")
	}
	return domain.Principal{
		Subject:     "operator",
		OrgID:       "org",
		Permissions: map[string]bool{"diagnostics.read": !a.denied},
	}, nil
}

type operatorLoginFixture struct {
	login     *OperatorLogin
	auth      *operatorTestAuth
	mux       *http.ServeMux
	mu        sync.Mutex
	challenge string
	exchanges int
	mode      string
}

func newOperatorFixture(t *testing.T, mode string) *operatorLoginFixture {
	t.Helper()
	f := &operatorLoginFixture{mode: mode, auth: &operatorTestAuth{denied: mode == "denied"}}
	var issuer *httptest.Server
	issuer = httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.serveIssuer(t, issuer.URL, w, r) }),
	)
	t.Cleanup(issuer.Close)
	var err error
	f.login, err = NewOperatorLogin(
		issuer.URL,
		"project",
		config.Operator{ClientID: "public-panel", RedirectURI: "http://localhost:58380/admin"},
	)
	require.NoError(t, err)
	f.mux = http.NewServeMux()
	(&server{auth: f.auth, options: Options{OperatorLogin: f.login}}).registerOperatorPanel(f.mux)
	return f
}
func (f *operatorLoginFixture) serveIssuer(t *testing.T, issuer string, w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		json.NewEncoder(w).
			Encode(map[string]string{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token"})
	case "/token":
		f.mu.Lock()
		f.exchanges++
		challenge := f.challenge
		f.mu.Unlock()
		r.ParseForm()
		hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(hash[:]) != challenge || r.Form.Get("client_id") != "public-panel" ||
			r.Form.Get("code") != "stub-code" ||
			r.Form.Get("redirect_uri") != "http://localhost:58380/admin" ||
			r.Form.Get("grant_type") != "authorization_code" ||
			r.Form.Has("client_secret") ||
			r.Header.Get("Authorization") != "" {
			t.Error("invalid PKCE public-client exchange")
		}
		if f.mode == "error" {
			http.Error(w, "private issuer detail", http.StatusBadRequest)
			return
		}
		token := "stub-access"
		if f.mode == "invalid" {
			token = "invalid-access"
		}
		json.NewEncoder(w).
			Encode(map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 300, "refresh_token": "must-never-return", "id_token": "must-never-return"})
	default:
		http.NotFound(w, r)
	}
}
func (f *operatorLoginFixture) start(t *testing.T) (string, *http.Cookie) {
	t.Helper()
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://localhost:58380/admin/login", nil))
	require.Equal(t, http.StatusFound, w.Code)
	u, err := url.Parse(w.Header().Get("Location"))
	require.NoError(t, err)
	q := u.Query()
	f.mu.Lock()
	f.challenge = q.Get("code_challenge")
	f.mu.Unlock()
	require.NotEmpty(t, q.Get("state"))
	require.Equal(t, "S256", q.Get("code_challenge_method"))
	require.Equal(t, "code", q.Get("response_type"))
	require.Equal(t, "public-panel", q.Get("client_id"))
	require.Contains(t, q.Get("scope"), "urn:zitadel:iam:org:project:id:project:aud")
	require.Contains(t, q.Get("scope"), "urn:zitadel:iam:org:project:roles")
	require.Contains(t, q.Get("scope"), "urn:zitadel:iam:user:resourceowner")
	c := w.Result().Cookies()[0]
	require.True(t, c.HttpOnly)
	require.Equal(t, http.SameSiteLaxMode, c.SameSite)
	require.Equal(t, operatorPanelPath, c.Path)
	require.Equal(t, 300, c.MaxAge)
	return q.Get("state"), c
}
func (f *operatorLoginFixture) exchange(state string, cookie *http.Cookie, origin string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"code": "stub-code", "state": state})
	r := httptest.NewRequest(
		http.MethodPost,
		"http://localhost:58380/admin/login/exchange",
		strings.NewReader(string(body)),
	)
	r.Header.Set("Origin", origin)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}
func (f *operatorLoginFixture) exchangeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exchanges
}
func TestOperatorPKCELogin(t *testing.T) {
	t.Parallel()
	f := newOperatorFixture(t, "")
	state, cookie := f.start(t)
	w := f.exchange(state, cookie, "http://localhost:58380")
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "stub-access")
	require.NotContains(t, w.Body.String(), "must-never-return")
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.Equal(t, 1, f.auth.calls)
	require.Equal(t, http.StatusUnauthorized, f.exchange(state, cookie, "http://localhost:58380").Code)
	require.Equal(t, 1, f.exchangeCount())
}
func TestOperatorPKCECallbackDenial(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"origin", "missing cookie", "wrong cookie", "wrong state", "expired"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newOperatorFixture(t, "")
			state, cookie := f.start(t)
			origin := "http://localhost:58380"
			status := http.StatusUnauthorized
			switch mode {
			case "origin":
				origin = "https://attacker.example"
				status = http.StatusForbidden
			case "missing cookie":
				cookie = nil
			case "wrong cookie":
				cookie = &http.Cookie{Name: operatorLoginCookie, Value: "wrong"}
			case "wrong state":
				state = "wrong"
			case "expired":
				f.login.mu.Lock()
				p := f.login.pending[state]
				p.expires = time.Now().Add(-time.Second)
				f.login.pending[state] = p
				f.login.mu.Unlock()
			}
			require.Equal(t, status, f.exchange(state, cookie, origin).Code)
			require.Zero(t, f.exchangeCount())
		})
	}
}
func TestOperatorPKCEExchangeDenial(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		mode   string
		status int
	}{{"denied", http.StatusForbidden}, {"invalid", http.StatusUnauthorized}, {"error", http.StatusBadGateway}} {
		t.Run(tc.mode, func(t *testing.T) {
			t.Parallel()
			f := newOperatorFixture(t, tc.mode)
			state, cookie := f.start(t)
			w := f.exchange(state, cookie, "http://localhost:58380")
			require.Equal(t, tc.status, w.Code)
			require.NotContains(t, w.Body.String(), "stub-access")
			require.NotContains(t, w.Body.String(), "private issuer detail")
			require.Equal(t, http.StatusUnauthorized, f.exchange(state, cookie, "http://localhost:58380").Code)
		})
	}
}
func TestOperatorLoginDisabled(t *testing.T) {
	t.Parallel()
	login, err := NewOperatorLogin("", "", config.Operator{})
	require.NoError(t, err)
	require.Nil(t, login)
	mux := http.NewServeMux()
	(&server{}).registerOperatorPanel(mux)
	for _, tc := range []struct{ path, method string }{{"/admin/login", http.MethodGet}, {"/admin/login/exchange", http.MethodPost}} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		require.Equal(t, http.StatusServiceUnavailable, w.Code)
	}
}
func TestOperatorLoginDiscoveryTrust(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"issuer", "endpoint", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			var issuer *httptest.Server
			issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "redirect" {
					http.Redirect(w, r, "https://untrusted.example/discovery", http.StatusFound)
					return
				}
				advertised := issuer.URL
				endpoint := issuer.URL + "/token"
				if mode == "issuer" {
					advertised = "https://untrusted.example"
				}
				if mode == "endpoint" {
					endpoint = "https://untrusted.example/token"
				}
				json.NewEncoder(w).
					Encode(map[string]string{"issuer": advertised, "authorization_endpoint": issuer.URL + "/authorize", "token_endpoint": endpoint})
			}))
			defer issuer.Close()
			login, err := NewOperatorLogin(
				issuer.URL,
				"project",
				config.Operator{ClientID: "public", RedirectURI: "https://panel.example/admin"},
			)
			require.NoError(t, err)
			w := httptest.NewRecorder()
			login.start(w, httptest.NewRequest(http.MethodGet, "https://panel.example/admin/login", nil))
			require.Equal(t, http.StatusBadGateway, w.Code)
			require.Empty(t, login.pending)
			require.Empty(t, w.Header().Get("Location"))
		})
	}
}
