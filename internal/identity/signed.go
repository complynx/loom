package identity

import (
	"context"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// verifySignedToken leaves opaque tokens to introspection and checks JWT trust first.
func (a *Authenticator) verifySignedToken(ctx context.Context, token, endpoint string) (jwt.Claims, error) {
	var signed jwt.Claims
	if strings.Count(token, ".") != signedTokenSeparators {
		return signed, nil
	}
	parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.RS256, jose.ES256})
	if err != nil {
		return signed, ErrUnauthorized
	}
	keys, err := a.getKeys(ctx, endpoint, false)
	if err != nil {
		return signed, err
	}
	// Only unknown key IDs refresh early; forged signatures with a known ID do not.
	if len(parsed.Headers) != 1 {
		return signed, ErrUnauthorized
	}
	kid := parsed.Headers[0].KeyID
	if kid != "" && !containsKey(keys, kid) {
		keys, err = a.getKeys(ctx, endpoint, true)
		if err != nil {
			return signed, err
		}
	}
	if !verifyClaims(parsed, keys, kid, &signed) {
		return signed, ErrUnauthorized
	}
	if signed.Expiry == nil || signed.Subject == "" || signed.ValidateWithLeeway(
		jwt.Expected{Issuer: a.cfg.Issuer, AnyAudience: jwt.Audience{a.cfg.Audience}, Time: time.Now()}, 0,
	) != nil {
		return signed, ErrUnauthorized
	}
	return signed, nil
}

func verifyClaims(parsed *jwt.JSONWebToken, keys jose.JSONWebKeySet, kid string, signed *jwt.Claims) bool {
	for _, key := range keys.Keys {
		if kid != "" && key.KeyID != kid {
			continue
		}
		if key.Use != "" && key.Use != "sig" {
			continue
		}
		if claimsErr := parsed.Claims(key.Key, signed); claimsErr == nil {
			return true
		}
	}
	return false
}
