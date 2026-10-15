package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/domain"
)

const (
	identityRequestTimeout = 3 * time.Second
	signedTokenSeparators  = 2
	identityResponseLimit  = 1 << 20
)

var ErrUnauthorized = errors.New("authentication denied")
var ErrUnavailable = errors.New("identity provider unavailable")

// Binding describes current trusted deployment registration. Headers alone are not proof.
type Binding struct {
	Actor       string
	Permissions map[string]bool
}
type BindingResolver func(context.Context, *http.Request, domain.Principal) (Binding, error)

// ServiceClassifier uses fresh trusted deployment registration and durable service-identity markers.
type ServiceClassifier func(context.Context, domain.Principal) (bool, error)
type Authenticator struct {
	assertionSigner     jose.Signer
	cache               *permissionsCache
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
	if unmarshalErr := json.Unmarshal(b, &ss); unmarshalErr != nil {
		return unmarshalErr
	}
	*a = ss
	return nil
}
func New(cfg config.Identity, client *http.Client, binding BindingResolver) (*Authenticator, error) {
	var err error
	cfg, err = cfg.ResolveCredentials()
	if err != nil {
		return nil, err
	}
	if cfg.PermissionsCacheTTLSeconds < 0 || cfg.PermissionsCacheTTLSeconds > 300 {
		return nil, errors.New("permissions cache TTL must be between 0 and 300 seconds")
	}
	if cfg.Issuer == "" || cfg.Audience == "" || cfg.ClientID == "" ||
		(cfg.ClientSecret == "" && cfg.ClientPrivateKey == nil) {
		return nil, errors.New("identity trust root and introspection credentials required")
	}
	if validateIdentityURLErr := config.ValidateIdentityURL(cfg.Issuer); validateIdentityURLErr != nil {
		return nil, validateIdentityURLErr
	}
	if cfg.RefreshableExchangeEnabled {
		return nil, errors.New("refreshable exchange integration gate unsupported")
	}
	if client == nil {
		client = &http.Client{}
	}
	bounded := *client
	bounded.Timeout = identityRequestTimeout
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	a := &Authenticator{cfg: cfg, client: &bounded, binding: binding,
		cache: newPermissionsCache(time.Duration(cfg.PermissionsCacheTTLSeconds) * time.Second)}
	if cfg.ClientPrivateKey != nil {
		a.assertionSigner, err = jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: cfg.ClientPrivateKey},
			(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", cfg.ClientKeyID))
		if err != nil {
			return nil, errors.New("cannot initialize introspection signer")
		}
	}
	return a, nil
}
func (a *Authenticator) Authenticate(ctx context.Context, r *http.Request) (domain.Principal, error) {
	token, err := bearer(r.Header.Get("Authorization"))
	if err != nil {
		return domain.Principal{}, err
	}
	p, err := a.authenticateToken(ctx, token)
	if err != nil {
		return domain.Principal{}, err
	}
	service, err := a.classifyService(ctx, p)
	if err != nil {
		return domain.Principal{}, err
	}
	delegated := r.Header.Get("X-Subject-Token")
	if !service {
		if delegated != "" {
			return domain.Principal{}, ErrUnauthorized
		}
		return p, nil
	}
	sessionID := r.Header.Get("X-Auth-Session-Id")
	if delegated == "" && sessionID != "" {
		return a.authenticateSession(ctx, r, p, sessionID)
	}
	if delegated != "" && sessionID != "" {
		return domain.Principal{}, ErrUnauthorized
	}
	if delegated == "" || a.binding == nil {
		return domain.Principal{}, ErrUnauthorized
	}
	user, err := a.authenticateToken(ctx, delegated)
	if err != nil {
		return domain.Principal{}, err
	}
	delegatedService, err := a.classifyService(ctx, user)
	if err != nil {
		return domain.Principal{}, err
	}
	if delegatedService || user.OrgID == "" || user.OrgID != p.OrgID {
		return domain.Principal{}, ErrUnauthorized
	}
	bound, err := a.binding(ctx, r, p)
	if err != nil {
		return domain.Principal{}, err
	}
	if bound.Actor != p.Subject {
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
	return slices.Contains(a.cfg.ServiceSubjects, subject)
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
	info, err := a.cache.load(ctx, token, a.introspectToken)
	if err != nil {
		return domain.Principal{}, err
	}
	return domain.Principal{
		Issuer: info.Issuer, Subject: info.Subject, Actor: info.Subject,
		OrgID: info.OrgID, Permissions: a.rolePermissions(info), ExpiresAt: time.Unix(info.Expires, 0),
	}, nil
}

func (a *Authenticator) introspectToken(ctx context.Context, token string) (tokenInfo, error) {
	d, err := a.discover(ctx)
	if err != nil {
		return tokenInfo{}, err
	}
	signed, err := a.verifySignedToken(ctx, token, d.JWKSURI)
	if err != nil {
		return tokenInfo{}, err
	}
	form := url.Values{"token": {token}, "token_type_hint": {"access_token"}}
	if err = a.addClientAssertion(form); err != nil {
		return tokenInfo{}, err
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		d.IntrospectionEndpoint,
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return tokenInfo{}, ErrUnavailable
	}
	if a.assertionSigner == nil {
		req.SetBasicAuth(a.cfg.ClientID, a.cfg.ClientSecret)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var info tokenInfo
	if fetchErr := a.fetch(req, &info); fetchErr != nil {
		return tokenInfo{}, fetchErr
	}
	now := time.Now()
	if !info.Active || info.Subject == "" || info.Issuer != a.cfg.Issuer || info.Expires <= now.Unix() ||
		info.NotBefore > now.Unix() {
		return tokenInfo{}, ErrUnauthorized
	}
	allowed := false
	for _, aud := range info.Audience {
		if aud == a.cfg.Audience {
			allowed = true
		}
	}
	if !allowed || signed.Subject != "" && signed.Subject != info.Subject {
		return tokenInfo{}, ErrUnauthorized
	}
	if signed.Expiry != nil && int64(*signed.Expiry) < info.Expires {
		info.Expires = int64(*signed.Expiry)
	}
	return info, nil
}

func (a *Authenticator) rolePermissions(info tokenInfo) map[string]bool {
	perms := map[string]bool{}
	for role, orgs := range info.Roles {
		if info.OrgID == "" {
			continue
		}
		if _, ok := orgs[info.OrgID]; !ok {
			continue
		}
		for _, permission := range a.cfg.RolePermissions[role] {
			perms[permission] = true
		}
	}
	return perms
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
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		strings.TrimSuffix(a.cfg.Issuer, "/")+"/.well-known/openid-configuration",
		nil,
	)
	if err != nil {
		return d, ErrUnavailable
	}
	if fetchErr := a.fetch(req, &d); fetchErr != nil {
		return d, fetchErr
	}
	if d.Issuer != a.cfg.Issuer || !trustedURL(d.JWKSURI, a.cfg.Issuer) ||
		!trustedURL(d.IntrospectionEndpoint, a.cfg.Issuer) {
		return discovery{}, ErrUnauthorized
	}
	a.mu.Lock()
	a.discovery = d
	a.mu.Unlock()
	return d, nil
}
func trustedURL(endpoint, issuer string) bool {
	u, err := url.Parse(endpoint)
	root, rootErr := url.Parse(issuer)
	return err == nil && rootErr == nil && u.Scheme == root.Scheme && u.Host == root.Host && u.User == nil &&
		u.Fragment == "" &&
		u.RawQuery == ""
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return keys, ErrUnavailable
	}
	var fetched jose.JSONWebKeySet
	if fetchErr := a.fetch(req, &fetched); fetchErr != nil {
		a.nextKeyRefresh = time.Now().Add(time.Second)
		a.keyRefreshError = fetchErr
		return keys, fetchErr
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
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: request failed", ErrUnavailable)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: upstream status %d", ErrUnavailable, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, identityResponseLimit+1))
	if err != nil || len(b) > identityResponseLimit {
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
	for _, prefix := range []string{"runs.", "chat.", "artifacts.", "preferences.", "users.", "settings.", "billing.", "operations.", "auth.", "sessions.", "diagnostics."} {
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
	discovered, err := a.serviceClassifier(ctx, p)
	if err != nil {
		return false, err
	}
	return static || discovered, nil
}
