package identity

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/complynx/clx-scripter/internal/domain"
	"net/http"
	"time"
)

type SessionCipher struct {
	keyID    string
	audience string
	clientID string
	aead     cipher.AEAD
}
type envelope struct {
	Purpose    string `json:"purpose"`
	Audience   string `json:"audience"`
	ClientID   string `json:"client_id"`
	KeyID      string `json:"key_id"`
	Issuer     string `json:"issuer"`
	Subject    string `json:"subject"`
	Actor      string `json:"actor"`
	OrgID      string `json:"org_id"`
	ExpiresAt  int64  `json:"expires_at"`
	Ciphertext string `json:"ciphertext"`
}

func NewSessionCipher(keyID string, key []byte) (*SessionCipher, error) {
	if keyID == "" || len(key) != 32 {
		return nil, errors.New("AES256 session key and key ID required")
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	aead, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	return &SessionCipher{keyID: keyID, aead: aead}, nil
}
func (c *SessionCipher) Seal(p domain.Principal, token string) (json.RawMessage, error) {
	if token == "" || p.Issuer == "" || p.Subject == "" || p.Actor == "" || !p.ExpiresAt.After(time.Now()) {
		return nil, ErrUnauthorized
	}
	env := envelope{Purpose: "clx-harness-session-v1", Audience: c.audience, ClientID: c.clientID, KeyID: c.keyID, Issuer: p.Issuer, Subject: p.Subject, Actor: p.Actor, OrgID: p.OrgID, ExpiresAt: p.ExpiresAt.Unix()}
	aad, e := json.Marshal(env)
	if e != nil {
		return nil, e
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, e := rand.Read(nonce); e != nil {
		return nil, e
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(token), aad)
	env.Ciphertext = base64.StdEncoding.EncodeToString(sealed)
	return json.Marshal(env)
}
func (c *SessionCipher) Open(expected domain.Principal, raw json.RawMessage) (string, error) {
	var env envelope
	if json.Unmarshal(raw, &env) != nil || env.Purpose != "clx-harness-session-v1" || env.Audience != c.audience || env.ClientID != c.clientID || env.KeyID != c.keyID || env.Issuer != expected.Issuer || env.Subject != expected.Subject || env.Actor != expected.Actor || env.OrgID != expected.OrgID || env.ExpiresAt != expected.ExpiresAt.Unix() || env.ExpiresAt <= time.Now().Unix() {
		return "", ErrUnauthorized
	}
	sealed, e := base64.StdEncoding.DecodeString(env.Ciphertext)
	if e != nil || len(sealed) < c.aead.NonceSize()+c.aead.Overhead() {
		return "", ErrUnauthorized
	}
	env.Ciphertext = ""
	aad, e := json.Marshal(env)
	if e != nil {
		return "", ErrUnauthorized
	}
	nonce := sealed[:c.aead.NonceSize()]
	plaintext, e := c.aead.Open(nil, nonce, sealed[c.aead.NonceSize():], aad)
	if e != nil {
		return "", ErrUnauthorized
	}
	return string(plaintext), nil
}

// SealRequest captures both credentials for a delegated caller. Plaintext never enters the domain model.
func (c *SessionCipher) SealRequest(_ context.Context, r *http.Request, p domain.Principal) (json.RawMessage, error) {
	token, e := bearer(r.Header.Get("Authorization"))
	if e != nil {
		return nil, e
	}
	subject := ""
	if p.Actor != p.Subject {
		subject = r.Header.Get("X-Subject-Token")
		if subject == "" {
			return nil, ErrUnauthorized
		}
	}
	bundle, e := json.Marshal([]string{token, subject})
	if e != nil {
		return nil, e
	}
	return c.Seal(p, string(bundle))
}

// VerifySession rechecks live access tokens. A service session also requires a current deployment registration check.
func (a *Authenticator) VerifySession(ctx context.Context, c *SessionCipher, expected domain.Principal, raw json.RawMessage, checkService func(context.Context, domain.Principal) (Binding, error)) (domain.Principal, string, error) {
	plain, e := c.Open(expected, raw)
	if e != nil {
		return domain.Principal{}, "", e
	}
	var tokens []string
	if json.Unmarshal([]byte(plain), &tokens) != nil || len(tokens) != 2 {
		return domain.Principal{}, "", ErrUnauthorized
	}
	userToken := tokens[0]
	var actor domain.Principal
	var binding Binding
	if expected.Actor != expected.Subject {
		if checkService == nil {
			return domain.Principal{}, "", ErrUnauthorized
		}
		actor, e = a.authenticateToken(ctx, tokens[0])
		if e != nil {
			return domain.Principal{}, "", e
		}
		actorService, e := a.classifyService(ctx, actor)
		if e != nil {
			return domain.Principal{}, "", e
		}
		if actor.Subject != expected.Actor || actor.OrgID != expected.OrgID || !actorService {
			return domain.Principal{}, "", ErrUnauthorized
		}
		binding, e = checkService(ctx, actor)
		if e != nil || binding.Actor != actor.Subject {
			return domain.Principal{}, "", ErrUnauthorized
		}
		userToken = tokens[1]
	}
	user, e := a.authenticateToken(ctx, userToken)
	if e != nil {
		return domain.Principal{}, "", e
	}
	userService, e := a.classifyService(ctx, user)
	if e != nil {
		return domain.Principal{}, "", e
	}
	if userService || user.Issuer != expected.Issuer || user.Subject != expected.Subject || user.OrgID != expected.OrgID {
		return domain.Principal{}, "", ErrUnauthorized
	}
	user.Actor = expected.Actor
	if actor.Subject != "" {
		user.Permissions = delegatedPermissions(user.Permissions, actor.Permissions, binding.Permissions)
		if actor.ExpiresAt.Before(user.ExpiresAt) {
			user.ExpiresAt = actor.ExpiresAt
		}
	}
	return user, userToken, nil
}

// NewSessionCipherForClient binds stored credentials to the configured OAuth client and audience.
func NewSessionCipherForClient(keyID string, key []byte, audience, clientID string) (*SessionCipher, error) {
	if audience == "" || clientID == "" {
		return nil, errors.New("audience and client ID required")
	}
	c, e := NewSessionCipher(keyID, key)
	if e != nil {
		return nil, e
	}
	c.audience = audience
	c.clientID = clientID
	return c, nil
}

// SessionResolver loads only a session bound to the independently verified current actor.
type SessionResolver func(context.Context, string, domain.Principal) (domain.Principal, json.RawMessage, error)

// UseSessions configures persisted delegation before the authenticator serves requests.
func (a *Authenticator) UseSessions(c *SessionCipher, resolver SessionResolver, checkService func(context.Context, domain.Principal) (Binding, error)) error {
	if c == nil || resolver == nil || checkService == nil {
		return errors.New("session cipher, resolver and service registration check required")
	}
	a.sessionCipher = c
	a.sessionResolver = resolver
	a.sessionServiceCheck = checkService
	return nil
}
func (a *Authenticator) authenticateSession(ctx context.Context, r *http.Request, actor domain.Principal, id string) (domain.Principal, error) {
	if a.sessionCipher == nil || a.sessionResolver == nil || a.binding == nil || len(id) > 256 {
		return domain.Principal{}, ErrUnauthorized
	}
	binding, e := a.binding(ctx, r, actor)
	if e != nil || binding.Actor != actor.Subject {
		return domain.Principal{}, ErrUnauthorized
	}
	expected, envelope, e := a.sessionResolver(ctx, id, actor)
	if e != nil {
		return domain.Principal{}, e
	}
	if expected.Actor != actor.Subject || expected.Issuer != actor.Issuer || expected.OrgID != actor.OrgID {
		return domain.Principal{}, ErrUnauthorized
	}
	fresh, _, e := a.VerifySession(ctx, a.sessionCipher, expected, envelope, a.sessionServiceCheck)
	if e != nil {
		return domain.Principal{}, e
	}
	fresh.Permissions = delegatedPermissions(fresh.Permissions, actor.Permissions, binding.Permissions)
	if actor.ExpiresAt.Before(fresh.ExpiresAt) {
		fresh.ExpiresAt = actor.ExpiresAt
	}
	return fresh, nil
}
