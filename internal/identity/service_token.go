package identity

import (
	"context"
	"crypto/sha256"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/complynx/loom/internal/domain"
)

const (
	serviceTokenEntries = 10000
	// serviceTokenSkew is the remaining lifetime below which a cached token is replaced.
	serviceTokenSkew = 30 * time.Second
	// serviceTokenClockLeeway accepts a token that the issuer made a moment ago
	// when the issuer's clock is ahead. It does not extend the expiry.
	serviceTokenClockLeeway = 30 * time.Second
	// issuedTypeJWT is the type that Loom requests from the issuer and requires in its response.
	issuedTypeJWT = "urn:ietf:params:oauth:token-type:jwt"
)

// ServiceTokenExchange configures the token exchange for downstream services.
type ServiceTokenExchange struct {
	ClientID, ClientSecret string
	// Audiences are requested for the service token and must all be present in it.
	Audiences []string
	Scopes    []string
	// Forbidden lists audiences that a service token must not contain: a token
	// that Loom sends to a service must not open Loom itself.
	Forbidden []string
}

type serviceToken struct {
	token   string
	expires time.Time
}

// ServiceTokens gets a separate token for downstream services in exchange for an
// external agent's verified token (RFC 8693). The agent never receives that
// token, so it cannot call a service outside Loom.
type ServiceTokens struct {
	auth  *Authenticator
	cfg   ServiceTokenExchange
	mu    sync.Mutex
	cache map[[sha256.Size]byte]serviceToken
}

// NewServiceTokens uses the discovery, key and HTTP settings of auth.
func NewServiceTokens(auth *Authenticator, cfg ServiceTokenExchange) *ServiceTokens {
	return &ServiceTokens{auth: auth, cfg: cfg, cache: map[[sha256.Size]byte]serviceToken{}}
}

// serviceTokenKey binds a cached token to the agent's token and to the
// permissions that Loom saw for it. After a role change Loom gets a new service
// token, so a service does not decide by older roles than Loom.
func serviceTokenKey(caller domain.ExternalCaller) [sha256.Size]byte {
	hash := sha256.New()
	hash.Write([]byte(caller.Token))
	for _, permission := range slices.Sorted(maps.Keys(caller.Principal.Permissions)) {
		if caller.Principal.Permissions[permission] {
			hash.Write([]byte{0})
			hash.Write([]byte(permission))
		}
	}
	return [sha256.Size]byte(hash.Sum(nil))
}

// For returns the service token of a caller whose token was verified in this
// request. A cached token is reused until it is close to expiry.
func (s *ServiceTokens) For(ctx context.Context, caller domain.ExternalCaller) (string, error) {
	key := serviceTokenKey(caller)
	now := time.Now()
	s.mu.Lock()
	cached, ok := s.cache[key]
	s.mu.Unlock()
	if ok && cached.expires.Sub(now) > serviceTokenSkew {
		return cached.token, nil
	}
	fresh, err := s.exchange(ctx, caller)
	if err != nil {
		return "", err
	}
	if !caller.Principal.ExpiresAt.IsZero() && caller.Principal.ExpiresAt.Before(fresh.expires) {
		fresh.expires = caller.Principal.ExpiresAt
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.cache) >= serviceTokenEntries {
		for other, entry := range s.cache {
			if !entry.expires.After(now) {
				delete(s.cache, other)
			}
		}
		if len(s.cache) >= serviceTokenEntries {
			clear(s.cache)
		}
	}
	s.cache[key] = fresh
	return fresh.token, nil
}

func (s *ServiceTokens) exchange(ctx context.Context, caller domain.ExternalCaller) (serviceToken, error) {
	a := s.auth
	d, err := a.discover(ctx)
	if err != nil {
		return serviceToken{}, err
	}
	if !trustedURL(d.TokenEndpoint, a.cfg.Issuer) {
		return serviceToken{}, ErrUnavailable
	}
	form := url.Values{
		"grant_type":           {"urn:ietf:params:oauth:grant-type:token-exchange"},
		"subject_token":        {caller.Token},
		"subject_token_type":   {"urn:ietf:params:oauth:token-type:access_token"},
		"requested_token_type": {issuedTypeJWT},
		"audience":             s.cfg.Audiences,
	}
	if len(s.cfg.Scopes) > 0 {
		form.Set("scope", strings.Join(s.cfg.Scopes, " "))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return serviceToken{}, ErrUnavailable
	}
	// OAuth client credentials are form-encoded before Basic encoding (RFC 6749, section 2.3.1).
	req.SetBasicAuth(url.QueryEscape(s.cfg.ClientID), url.QueryEscape(s.cfg.ClientSecret))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// expires_in is optional (RFC 8693, section 2.2.1). The signed expiry is always required.
	var response struct {
		Token   string `json:"access_token"`
		Issued  string `json:"issued_token_type"`
		Type    string `json:"token_type"`
		Expires *int64 `json:"expires_in"`
	}
	if err = a.fetch(req, &response); err != nil {
		return serviceToken{}, err
	}
	if response.Token == "" || response.Issued != issuedTypeJWT || !strings.EqualFold(response.Type, "Bearer") ||
		response.Expires != nil && *response.Expires <= 0 {
		return serviceToken{}, ErrUnauthorized
	}
	expires, err := s.verify(ctx, d.JWKSURI, response.Token, caller.Principal)
	if err != nil {
		return serviceToken{}, err
	}
	if response.Expires != nil {
		if limit := time.Now().Add(time.Duration(*response.Expires) * time.Second); limit.Before(expires) {
			expires = limit
		}
	}
	return serviceToken{token: response.Token, expires: expires}, nil
}

// verify checks that the issuer signed the service token for the same user and
// organization and for the requested services only. It returns the token expiry.
func (s *ServiceTokens) verify(
	ctx context.Context,
	keysEndpoint, token string,
	user domain.Principal,
) (time.Time, error) {
	a := s.auth
	parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.RS256, jose.ES256})
	if err != nil || len(parsed.Headers) != 1 {
		return time.Time{}, ErrUnauthorized
	}
	keys, err := a.getKeys(ctx, keysEndpoint, false)
	if err != nil {
		return time.Time{}, err
	}
	kid := parsed.Headers[0].KeyID
	if kid != "" && !containsKey(keys, kid) {
		if keys, err = a.getKeys(ctx, keysEndpoint, true); err != nil {
			return time.Time{}, err
		}
	}
	var claims jwt.Claims
	if !verifyClaims(parsed, keys, kid, &claims) {
		return time.Time{}, ErrUnauthorized
	}
	// The signature is verified above, so the other claims of this token are trusted.
	var owner struct {
		OrgID string `json:"urn:zitadel:iam:user:resourceowner:id"`
	}
	if parsed.UnsafeClaimsWithoutVerification(&owner) != nil {
		return time.Time{}, ErrUnauthorized
	}
	now := time.Now()
	// A service decides by the organization in its token. When the token names
	// one, it must be the organization that Loom used for admission.
	if claims.Expiry == nil || !claims.Expiry.Time().After(now) || claims.Subject != user.Subject ||
		owner.OrgID != "" && owner.OrgID != user.OrgID ||
		claims.ValidateWithLeeway(jwt.Expected{Issuer: a.cfg.Issuer, Time: now}, serviceTokenClockLeeway) != nil {
		return time.Time{}, ErrUnauthorized
	}
	for _, audience := range s.cfg.Audiences {
		if !claims.Audience.Contains(audience) {
			return time.Time{}, ErrUnauthorized
		}
	}
	if slices.ContainsFunc(s.cfg.Forbidden, claims.Audience.Contains) {
		return time.Time{}, ErrUnauthorized
	}
	return claims.Expiry.Time(), nil
}
