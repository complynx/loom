package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/complynx/clx-scripter/internal/config"
	"github.com/complynx/clx-scripter/internal/domain"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var ErrUnauthorized = errors.New("authentication denied")
var ErrUnavailable = errors.New("identity provider unavailable")

// BindingResolver consults current trusted deployment registration. Headers alone are not proof.
type Binding struct {
	Actor       string
	Permissions map[string]bool
}
type BindingResolver func(context.Context, *http.Request, domain.Principal) (Binding, error)

// ServiceClassifier uses fresh trusted deployment registration and durable service-identity markers.
type ServiceClassifier func(context.Context, domain.Principal) (bool, error)
type Authenticator struct {
	serviceClassifier   ServiceClassifier
	discoveryFetchMu    sync.Mutex
	keysFetchMu         sync.Mutex
	nextKeyRefresh      time.Time
	keyRefreshError     error
	sessionCipher       *SessionCipher
	sessionResolver     SessionResolver
	sessionServiceCheck func(context.Context, domain.Principal) (Binding, error)
	cfg                 config.Identity
	client              *http.Client
	binding             BindingResolver
	mu                  sync.Mutex
	keys                jose.JSONWebKeySet
	keysUntil           time.Time
	discovery           discovery
}
type discovery struct {
	Issuer                string `json:"issuer"`
	JWKSURI               string `json:"jwks_uri"`
	IntrospectionEndpoint string `json:"introspection_endpoint"`
}
type tokenInfo struct {
	Active    bool                         `json:"active"`
	Issuer    string                       `json:"iss"`
	Subject   string                       `json:"sub"`
	Audience  audience                     `json:"aud"`
	Expires   int64                        `json:"exp"`
	NotBefore int64                        `json:"nbf"`
	ClientID  string                       `json:"client_id"`
	OrgID     string                       `json:"urn:zitadel:iam:user:resourceowner:id"`
	Roles     map[string]map[string]string `json:"urn:zitadel:iam:org:project:roles"`
}
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*a = []string{s}
		return nil
	}
	var ss []string
	if e := json.Unmarshal(b, &ss); e != nil {
		return e
	}
	*a = ss
	return nil
}
func New(cfg config.Identity, client *http.Client, binding BindingResolver) (*Authenticator, error) {
	if cfg.Issuer == "" || cfg.Audience == "" || cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, errors.New("identity trust root and introspection credentials required")
	}
	if e := config.ValidateIdentityURL(cfg.Issuer); e != nil {
		return nil, e
	}
	if cfg.RefreshableExchangeEnabled {
		return nil, errors.New("refreshable exchange integration gate unsupported")
	}
	if client == nil {
		client = &http.Client{}
	}
	bounded := *client
	bounded.Timeout = 3 * time.Second
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Authenticator{cfg: cfg, client: &bounded, binding: binding}, nil
}
func (a *Authenticator) Authenticate(ctx context.Context, r *http.Request) (domain.Principal, error) {
	token, e := bearer(r.Header.Get("Authorization"))
	if e != nil {
		return domain.Principal{}, e
	}
	p, e := a.authenticateToken(ctx, token)
	if e != nil {
		return domain.Principal{}, e
	}
	service, e := a.classifyService(ctx, p)
	if e != nil {
		return domain.Principal{}, e
	}
	delegated := r.Header.Get("X-Subject-Token")
	if !service {
		if delegated != "" {
			return domain.Principal{}, ErrUnauthorized
		}
		return p, nil
	}
	sessionID := r.Header.Get("X-Auth-Session-ID")
	if delegated == "" && sessionID != "" {
		return a.authenticateSession(ctx, r, p, sessionID)
	}
	if delegated != "" && sessionID != "" {
		return domain.Principal{}, ErrUnauthorized
	}
	if delegated == "" || a.binding == nil {
		return domain.Principal{}, ErrUnauthorized
	}
	bound, e := a.binding(ctx, r, p)
	if e != nil || bound.Actor != p.Subject {
		return domain.Principal{}, ErrUnauthorized
	}
	user, e := a.authenticateToken(ctx, delegated)
	if e != nil {
		return domain.Principal{}, e
	}
	delegatedService, e := a.classifyService(ctx, user)
	if e != nil {
		return domain.Principal{}, e
	}
	if delegatedService || user.OrgID == "" || user.OrgID != p.OrgID {
		return domain.Principal{}, ErrUnauthorized
	}
	user.Actor = p.Subject
	user.Permissions = delegatedPermissions(user.Permissions, p.Permissions, bound.Permissions)
	if p.ExpiresAt.Before(user.ExpiresAt) {
		user.ExpiresAt = p.ExpiresAt
	}
	return user, nil
}
func (a *Authenticator) isService(subject string) bool {
	for _, s := range a.cfg.ServiceSubjects {
		if s == subject {
			return true
		}
	}
	return false
}
func bearer(h string) (string, error) {
	v := strings.Fields(h)
	if len(v) != 2 || !strings.EqualFold(v[0], "Bearer") || len(v[1]) > 16384 {
		return "", ErrUnauthorized
	}
	return v[1], nil
}

// Credential may be used only after Authenticate succeeds for this request.
func Credential(r *http.Request, p domain.Principal) (string, error) {
	if p.Actor != p.Subject {
		t := r.Header.Get("X-Subject-Token")
		if t == "" || len(t) > 16384 {
			return "", ErrUnauthorized
		}
		return t, nil
	}
	return bearer(r.Header.Get("Authorization"))
}
func (a *Authenticator) authenticateToken(ctx context.Context, token string) (domain.Principal, error) {
	if token == "" || len(token) > 16384 {
		return domain.Principal{}, ErrUnauthorized
	}
	d, e := a.discover(ctx)
	if e != nil {
		return domain.Principal{}, e
	}
	var signed jwt.Claims
	if strings.Count(token, ".") == 2 {
		parsed, e := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.RS256, jose.ES256})
		if e != nil {
			return domain.Principal{}, ErrUnauthorized
		}
		keys, e := a.getKeys(ctx, d.JWKSURI, false)
		if e != nil {
			return domain.Principal{}, e
		}
		// Only an unknown key ID can request an early refresh; a forged signature using a known ID cannot.
		if len(parsed.Headers) != 1 {
			return domain.Principal{}, ErrUnauthorized
		}
		kid := parsed.Headers[0].KeyID
		if kid != "" && !containsKey(keys, kid) {
			keys, e = a.getKeys(ctx, d.JWKSURI, true)
			if e != nil {
				return domain.Principal{}, e
			}
		}
		valid := false
		for _, key := range keys.Keys {
			if kid != "" && key.KeyID != kid {
				continue
			}
			if key.Use != "" && key.Use != "sig" {
				continue
			}
			if e := parsed.Claims(key.Key, &signed); e == nil {
				valid = true
				break
			}
		}
		if !valid {
			return domain.Principal{}, ErrUnauthorized
		}
		if signed.Expiry == nil || signed.Subject == "" || signed.ValidateWithLeeway(jwt.Expected{Issuer: a.cfg.Issuer, AnyAudience: jwt.Audience{a.cfg.Audience}, Time: time.Now()}, 0) != nil {
			return domain.Principal{}, ErrUnauthorized
		}
	}
	form := url.Values{"token": {token}, "token_type_hint": {"access_token"}}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, d.IntrospectionEndpoint, strings.NewReader(form.Encode()))
	if e != nil {
		return domain.Principal{}, ErrUnavailable
	}
	req.SetBasicAuth(a.cfg.ClientID, a.cfg.ClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var info tokenInfo
	if e := a.fetch(req, &info); e != nil {
		return domain.Principal{}, e
	}
	now := time.Now()
	if !info.Active || info.Subject == "" || info.Issuer != a.cfg.Issuer || info.Expires <= now.Unix() || info.NotBefore > now.Unix() {
		return domain.Principal{}, ErrUnauthorized
	}
	allowed := false
	for _, aud := range info.Audience {
		if aud == a.cfg.Audience {
			allowed = true
		}
	}
	if !allowed || signed.Subject != "" && signed.Subject != info.Subject {
		return domain.Principal{}, ErrUnauthorized
	}
	if signed.Expiry != nil && int64(*signed.Expiry) < info.Expires {
		info.Expires = int64(*signed.Expiry)
	}
	perms := map[string]bool{}
	for role, orgs := range info.Roles {
		if info.OrgID == "" {
			continue
		}
		if _, ok := orgs[info.OrgID]; !ok {
			continue
		}
		for _, p := range a.cfg.RolePermissions[role] {
			perms[p] = true
		}
	}
	return domain.Principal{Issuer: info.Issuer, Subject: info.Subject, Actor: info.Subject, OrgID: info.OrgID, Permissions: perms, ExpiresAt: time.Unix(info.Expires, 0)}, nil
}
func (a *Authenticator) discover(ctx context.Context) (discovery, error) {
	a.mu.Lock()
	d := a.discovery
	a.mu.Unlock()
	if d.Issuer != "" {
		return d, nil
	}
	a.discoveryFetchMu.Lock()
	defer a.discoveryFetchMu.Unlock()
	a.mu.Lock()
	d = a.discovery
	a.mu.Unlock()
	if d.Issuer != "" {
		return d, nil
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(a.cfg.Issuer, "/")+"/.well-known/openid-configuration", nil)
	if e != nil {
		return d, ErrUnavailable
	}
	if e := a.fetch(req, &d); e != nil {
		return d, e
	}
	if d.Issuer != a.cfg.Issuer || !trustedURL(d.JWKSURI, a.cfg.Issuer) || !trustedURL(d.IntrospectionEndpoint, a.cfg.Issuer) {
		return discovery{}, ErrUnauthorized
	}
	a.mu.Lock()
	a.discovery = d
	a.mu.Unlock()
	return d, nil
}
func trustedURL(endpoint, issuer string) bool {
	u, e := url.Parse(endpoint)
	root, e2 := url.Parse(issuer)
	return e == nil && e2 == nil && u.Scheme == root.Scheme && u.Host == root.Host && u.User == nil && u.Fragment == "" && u.RawQuery == ""
}
func containsKey(keys jose.JSONWebKeySet, id string) bool {
	for _, key := range keys.Keys {
		if key.KeyID == id {
			return true
		}
	}
	return false
}

// Fetch serialization and a one-second early-refresh cooldown bound requests caused by unknown key IDs.
func (a *Authenticator) getKeys(ctx context.Context, endpoint string, force bool) (jose.JSONWebKeySet, error) {
	a.mu.Lock()
	keys, until := a.keys, a.keysUntil
	a.mu.Unlock()
	if !force && time.Now().Before(until) {
		return keys, nil
	}
	a.keysFetchMu.Lock()
	defer a.keysFetchMu.Unlock()
	a.mu.Lock()
	keys, until = a.keys, a.keysUntil
	a.mu.Unlock()
	if !force && time.Now().Before(until) {
		return keys, nil
	}
	if time.Now().Before(a.nextKeyRefresh) {
		return keys, a.keyRefreshError
	}
	if force {
		a.nextKeyRefresh = time.Now().Add(time.Second)
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if e != nil {
		return keys, ErrUnavailable
	}
	var fetched jose.JSONWebKeySet
	if e := a.fetch(req, &fetched); e != nil {
		a.nextKeyRefresh = time.Now().Add(time.Second)
		a.keyRefreshError = e
		return keys, e
	}
	if len(fetched.Keys) == 0 || len(fetched.Keys) > 64 {
		a.nextKeyRefresh = time.Now().Add(time.Second)
		a.keyRefreshError = ErrUnavailable
		return keys, ErrUnavailable
	}
	a.keyRefreshError = nil
	ttl := a.cfg.JWKSCacheTTLSeconds
	if ttl <= 0 {
		ttl = 300
	}
	a.mu.Lock()
	a.keys = fetched
	a.keysUntil = time.Now().Add(time.Duration(ttl) * time.Second)
	a.mu.Unlock()
	return fetched, nil
}
func (a *Authenticator) fetch(req *http.Request, out any) error {
	resp, e := a.client.Do(req)
	if e != nil {
		return fmt.Errorf("%w: request failed", ErrUnavailable)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: upstream status %d", ErrUnavailable, resp.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, 1048577))
	if e != nil || len(b) > 1048576 {
		return ErrUnavailable
	}
	if json.Unmarshal(b, out) != nil {
		return ErrUnavailable
	}
	return nil
}

// Harness operation rights belong to the calling service; module categories belong to the delegated user.
func delegatedPermissions(user, actor, binding map[string]bool) map[string]bool {
	out := map[string]bool{}
	for p, allowed := range user {
		if allowed && !harnessPermission(p) {
			out[p] = true
		}
	}
	for p, allowed := range actor {
		if allowed && harnessPermission(p) && binding[p] {
			out[p] = true
		}
	}
	return out
}
func harnessPermission(p string) bool {
	for _, prefix := range []string{"runs.", "chat.", "artifacts.", "preferences.", "users.", "settings.", "billing.", "operations.", "auth.", "sessions."} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// UseServiceClassifier must be called before the authenticator serves requests.
func (a *Authenticator) UseServiceClassifier(classifier ServiceClassifier) error {
	if classifier == nil {
		return errors.New("service classifier required")
	}
	a.serviceClassifier = classifier
	return nil
}
func (a *Authenticator) classifyService(ctx context.Context, p domain.Principal) (bool, error) {
	static := a.isService(p.Subject)
	if a.serviceClassifier == nil {
		return static, nil
	}
	discovered, e := a.serviceClassifier(ctx, p)
	if e != nil {
		return false, e
	}
	return static || discovered, nil
}
