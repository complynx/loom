package identity

import (
	"context"
	"crypto/sha256"
	"maps"
	"slices"
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
)

// ServiceTokenSource configures where service tokens come from.
type ServiceTokenSource struct {
	// Fetch asks the named token service for the service token of the user who
	// owns subjectToken. expiresIn is nil when the service states no lifetime.
	Fetch func(ctx context.Context, service, subjectToken string) (token string, expiresIn *int64, err error)
	// Forbidden lists audiences that a service token must not contain: a token
	// that Loom sends to a service must not open Loom itself.
	Forbidden []string
}

type serviceToken struct {
	token   string
	expires time.Time
}

// ServiceTokens gets a separate token for downstream services from a token
// service, in exchange for an external agent's verified token. The agent never
// receives that token, so it cannot call a service outside Loom.
type ServiceTokens struct {
	auth  *Authenticator
	cfg   ServiceTokenSource
	mu    sync.Mutex
	cache map[[sha256.Size]byte]serviceToken
}

// NewServiceTokens verifies service tokens with the issuer and keys of auth.
func NewServiceTokens(auth *Authenticator, cfg ServiceTokenSource) *ServiceTokens {
	return &ServiceTokens{auth: auth, cfg: cfg, cache: map[[sha256.Size]byte]serviceToken{}}
}

// serviceTokenKey binds a cached token to the token service, to the agent's
// token and to the permissions that Loom saw for it. After a role change Loom
// gets a new service token, so a service does not decide by older roles than Loom.
func serviceTokenKey(caller domain.ExternalCaller, service string) [sha256.Size]byte {
	hash := sha256.New()
	hash.Write([]byte(service))
	hash.Write([]byte{0})
	hash.Write([]byte(caller.Token))
	for _, permission := range slices.Sorted(maps.Keys(caller.Principal.Permissions)) {
		if caller.Principal.Permissions[permission] {
			hash.Write([]byte{0})
			hash.Write([]byte(permission))
		}
	}
	return [sha256.Size]byte(hash.Sum(nil))
}

// For returns the token that the named token service issues for a caller whose
// token was verified in this request. A cached token is reused until it is
// close to expiry.
func (s *ServiceTokens) For(ctx context.Context, caller domain.ExternalCaller, service string) (string, error) {
	key := serviceTokenKey(caller, service)
	now := time.Now()
	s.mu.Lock()
	cached, ok := s.cache[key]
	s.mu.Unlock()
	if ok && cached.expires.Sub(now) > serviceTokenSkew {
		return cached.token, nil
	}
	fresh, err := s.issue(ctx, caller, service)
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

func (s *ServiceTokens) issue(ctx context.Context, caller domain.ExternalCaller, service string) (serviceToken, error) {
	token, expiresIn, err := s.cfg.Fetch(ctx, service, caller.Token)
	if err != nil {
		return serviceToken{}, err
	}
	if token == "" || expiresIn != nil && *expiresIn <= 0 {
		return serviceToken{}, ErrUnauthorized
	}
	d, err := s.auth.discover(ctx)
	if err != nil {
		return serviceToken{}, err
	}
	expires, err := s.verify(ctx, d.JWKSURI, token, caller.Principal)
	if err != nil {
		return serviceToken{}, err
	}
	// The signed expiry always applies. A stated lifetime can only shorten it.
	if expiresIn != nil {
		if limit := time.Now().Add(time.Duration(*expiresIn) * time.Second); limit.Before(expires) {
			expires = limit
		}
	}
	return serviceToken{token: token, expires: expires}, nil
}

// verify checks that the issuer signed the service token for the same user and
// organization, and that the token does not open Loom. A token service is not
// trusted for these facts. It returns the token expiry.
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
	if slices.ContainsFunc(s.cfg.Forbidden, claims.Audience.Contains) {
		return time.Time{}, ErrUnauthorized
	}
	return claims.Expiry.Time(), nil
}
