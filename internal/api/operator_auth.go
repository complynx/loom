package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/complynx/loom/internal/config"
)

const operatorLoginCookie = "clx_operator_login"
const operatorLoginTTL = 5 * time.Minute
const operatorPanelPath = "/admin"
const operatorRandomBytes = 32
const operatorPendingLimit = 1024
const operatorCallbackLimit = 8192
const operatorHTTPTimeout = 5 * time.Second
const operatorIssuerResponseLimit = 1 << 20

type operatorPending struct {
	verifier, binding, tokenEndpoint string
	expires                          time.Time
}

// OperatorLogin holds short-lived single-use PKCE transactions, never access tokens.
type OperatorLogin struct {
	issuer, audience, clientID, redirectURI, origin string
	secure                                          bool
	client                                          *http.Client
	mu                                              sync.Mutex
	pending                                         map[string]operatorPending
}

// NewOperatorLogin performs no network requests. An empty client disables login.
func NewOperatorLogin(issuer, audience string, cfg config.Operator) (*OperatorLogin, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.ClientID == "" {
		return nil, nil //nolint:nilnil // An absent optional login component is the documented disabled state.
	}
	if err := config.ValidateIdentityURL(issuer); err != nil {
		return nil, err
	}
	if audience == "" || strings.ContainsAny(audience, " \t\r\n") {
		return nil, errors.New("operator audience required")
	}
	u, _ := url.Parse(cfg.RedirectURI)
	return &OperatorLogin{
		issuer:      strings.TrimRight(issuer, "/"),
		audience:    audience,
		clientID:    cfg.ClientID,
		redirectURI: cfg.RedirectURI,
		origin:      u.Scheme + "://" + u.Host,
		secure:      u.Scheme == "https",
		client: &http.Client{
			Timeout:       operatorHTTPTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		},
		pending: map[string]operatorPending{},
	}, nil
}
func operatorRandom() (string, error) {
	b := make([]byte, operatorRandomBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func (s *server) registerOperatorLogin(mux *http.ServeMux) {
	l := s.options.OperatorLogin
	mux.HandleFunc("GET /admin/login/config", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		write(w, http.StatusOK, map[string]bool{"enabled": l != nil})
	})
	mux.HandleFunc("GET /admin/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if l == nil {
			http.Error(w, "Operator login is not configured", http.StatusServiceUnavailable)
			return
		}
		l.start(w, r)
	})
	mux.HandleFunc("POST /admin/login/exchange", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if l == nil {
			http.Error(w, "Operator login is not configured", http.StatusServiceUnavailable)
			return
		}
		l.exchange(w, r, s.auth)
	})
}
func (l *OperatorLogin) discovery(ctx context.Context) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.issuer+"/.well-known/openid-configuration", nil)
	if err != nil {
		return "", "", err
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	var d struct {
		Issuer        string `json:"issuer"`
		Authorization string `json:"authorization_endpoint"`
		Token         string `json:"token_endpoint"`
	}
	if resp.StatusCode != http.StatusOK ||
		json.NewDecoder(io.LimitReader(resp.Body, operatorIssuerResponseLimit)).Decode(&d) != nil ||
		strings.TrimRight(d.Issuer, "/") != l.issuer {
		return "", "", errors.New("invalid discovery")
	}
	issuer, _ := url.Parse(l.issuer)
	for _, endpoint := range []string{d.Authorization, d.Token} {
		if endpointErr := config.ValidateIdentityURL(endpoint); endpointErr != nil {
			return "", "", endpointErr
		}
		u, _ := url.Parse(endpoint)
		if u.Scheme != issuer.Scheme || u.Host != issuer.Host {
			return "", "", errors.New("issuer endpoint origin mismatch")
		}
	}
	return d.Authorization, d.Token, nil
}
func (l *OperatorLogin) start(w http.ResponseWriter, r *http.Request) {
	authorization, endpoint, err := l.discovery(r.Context())
	if err != nil {
		http.Error(w, "Identity provider unavailable", http.StatusBadGateway)
		return
	}
	state, err := operatorRandom()
	if err != nil {
		http.Error(w, "Login unavailable", http.StatusInternalServerError)
		return
	}
	verifier, err := operatorRandom()
	if err != nil {
		http.Error(w, "Login unavailable", http.StatusInternalServerError)
		return
	}
	binding, err := operatorRandom()
	if err != nil {
		http.Error(w, "Login unavailable", http.StatusInternalServerError)
		return
	}
	l.mu.Lock()
	for key, pending := range l.pending {
		if !time.Now().Before(pending.expires) {
			delete(l.pending, key)
		}
	}
	if len(l.pending) >= operatorPendingLimit {
		l.mu.Unlock()
		http.Error(w, "Login capacity reached; try again later", http.StatusServiceUnavailable)
		return
	}
	l.pending[state] = operatorPending{
		verifier:      verifier,
		binding:       binding,
		tokenEndpoint: endpoint,
		expires:       time.Now().Add(operatorLoginTTL),
	}
	l.mu.Unlock()
	http.SetCookie(
		w,
		// #nosec G124 -- Secure is required on HTTPS; validated loopback HTTP supports local QA.
		&http.Cookie{
			Name:     operatorLoginCookie,
			Value:    binding,
			Path:     operatorPanelPath,
			HttpOnly: true,
			Secure:   l.secure,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   int(operatorLoginTTL.Seconds()),
		},
	)
	u, _ := url.Parse(authorization)
	q := u.Query()
	q.Set("client_id", l.clientID)
	q.Set("redirect_uri", l.redirectURI)
	q.Set("response_type", "code")
	q.Set("state", state)
	q.Set(
		"scope",
		"openid urn:zitadel:iam:user:resourceowner urn:zitadel:iam:org:project:id:"+l.audience+":aud urn:zitadel:iam:org:project:roles",
	)
	challenge := sha256.Sum256([]byte(verifier))
	q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:]))
	q.Set("code_challenge_method", "S256")
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}
func (l *OperatorLogin) exchange(w http.ResponseWriter, r *http.Request, auth Authenticator) {
	if r.Header.Get("Origin") != l.origin {
		http.Error(w, "Login origin denied", http.StatusForbidden)
		return
	}
	var input struct {
		Code  string `json:"code"`
		State string `json:"state"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, operatorCallbackLimit)).Decode(&input) != nil ||
		input.Code == "" ||
		input.State == "" {
		http.Error(w, "Invalid login callback", http.StatusBadRequest)
		return
	}
	cookie, err := r.Cookie(operatorLoginCookie)
	if err != nil {
		http.Error(w, "Login expired; sign in again", http.StatusUnauthorized)
		return
	}
	l.mu.Lock()
	pending, ok := l.pending[input.State]
	if ok && subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(pending.binding)) == 1 {
		delete(l.pending, input.State)
	} else {
		ok = false
	}
	l.mu.Unlock()
	if !ok || !time.Now().Before(pending.expires) {
		http.Error(w, "Login expired; sign in again", http.StatusUnauthorized)
		return
	}
	// #nosec G124 -- Secure is required on HTTPS; HTTP is validated as loopback-only for local QA.
	http.SetCookie(
		w,
		// #nosec G124 -- Secure is required on HTTPS; validated loopback HTTP supports local QA.
		&http.Cookie{
			Name:     operatorLoginCookie,
			Path:     operatorPanelPath,
			HttpOnly: true,
			Secure:   l.secure,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   -1,
		},
	)
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {l.clientID},
		"redirect_uri":  {l.redirectURI},
		"code":          {input.Code},
		"code_verifier": {pending.verifier},
	}
	req, err := http.NewRequestWithContext(
		r.Context(),
		http.MethodPost,
		pending.tokenEndpoint,
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		http.Error(w, "Login exchange failed", http.StatusBadGateway)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := l.client.Do(req)
	if err != nil {
		http.Error(w, "Login exchange failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	var result struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if resp.StatusCode != http.StatusOK ||
		json.NewDecoder(io.LimitReader(resp.Body, operatorIssuerResponseLimit)).Decode(&result) != nil ||
		result.AccessToken == "" ||
		!strings.EqualFold(result.TokenType, "Bearer") ||
		result.ExpiresIn <= 0 {
		http.Error(w, "Login exchange failed", http.StatusBadGateway)
		return
	}
	credential := r.Clone(r.Context())
	credential.Header = make(http.Header)
	credential.Header.Set("Authorization", "Bearer "+result.AccessToken)
	principal, err := auth.Authenticate(r.Context(), credential)
	if err != nil {
		http.Error(w, "Login authorization denied", http.StatusUnauthorized)
		return
	}
	if !principal.Has("diagnostics.read") {
		http.Error(w, "Diagnostics permission required", http.StatusForbidden)
		return
	}
	write(w, http.StatusOK, map[string]any{"access_token": result.AccessToken, "expires_in": result.ExpiresIn})
}
