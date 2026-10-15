package identity

import (
	"net/url"
	"time"

	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"
)

// addClientAssertion authenticates introspection as the registered API application.
func (a *Authenticator) addClientAssertion(form url.Values) error {
	if a.assertionSigner == nil {
		return nil
	}
	now := time.Now()
	assertion, err := jwt.Signed(a.assertionSigner).Claims(jwt.Claims{
		Issuer: a.cfg.ClientID, Subject: a.cfg.ClientID, Audience: jwt.Audience{a.cfg.Issuer},
		IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(time.Minute)), ID: uuid.NewString(),
	}).Serialize()
	if err != nil {
		return ErrUnavailable
	}
	form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
	form.Set("client_assertion", assertion)
	return nil
}
