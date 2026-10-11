package identity

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/complynx/loom/internal/domain"
)

type SessionCipher struct {
	keyID    string
	audience string
	clientID string
	aead     cipher.AEAD
	keys     map[string]cipher.AEAD
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
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &SessionCipher{keyID: keyID, aead: aead, keys: map[string]cipher.AEAD{keyID: aead}}, nil
}

// NewSessionCipherForClient binds stored credentials to the configured OAuth client and audience.
func NewSessionCipherForClient(keyID string, key []byte, audience, clientID string) (*SessionCipher, error) {
	if audience == "" || clientID == "" {
		return nil, errors.New("audience and client ID required")
	}
	c, err := NewSessionCipher(keyID, key)
	if err != nil {
		return nil, err
	}
	c.audience = audience
	c.clientID = clientID
	return c, nil
}

func (c *SessionCipher) Seal(p domain.Principal, token string) (json.RawMessage, error) {
	return c.seal(p, token, false)
}
func (c *SessionCipher) seal(p domain.Principal, token string, allowExpired bool) (json.RawMessage, error) {
	if token == "" || p.Issuer == "" || p.Subject == "" || p.Actor == "" ||
		(!allowExpired && !p.ExpiresAt.After(time.Now())) {
		return nil, ErrUnauthorized
	}
	env := envelope{
		Purpose:   "clx-harness-session-v1",
		Audience:  c.audience,
		ClientID:  c.clientID,
		KeyID:     c.keyID,
		Issuer:    p.Issuer,
		Subject:   p.Subject,
		Actor:     p.Actor,
		OrgID:     p.OrgID,
		ExpiresAt: p.ExpiresAt.Unix(),
	}
	aad, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, readErr := rand.Read(nonce); readErr != nil {
		return nil, readErr
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(token), aad)
	env.Ciphertext = base64.StdEncoding.EncodeToString(sealed)
	return json.Marshal(env)
}
func (c *SessionCipher) Open(expected domain.Principal, raw json.RawMessage) (string, error) {
	return c.open(expected, raw, false)
}
func (c *SessionCipher) open(expected domain.Principal, raw json.RawMessage, allowExpired bool) (string, error) {
	var env envelope
	if json.Unmarshal(raw, &env) != nil || env.Purpose != "clx-harness-session-v1" || env.Audience != c.audience ||
		env.ClientID != c.clientID ||
		env.Issuer != expected.Issuer ||
		env.Subject != expected.Subject ||
		env.Actor != expected.Actor ||
		env.OrgID != expected.OrgID ||
		env.ExpiresAt != expected.ExpiresAt.Unix() ||
		(!allowExpired && env.ExpiresAt <= time.Now().Unix()) {
		return "", ErrUnauthorized
	}
	aead, ok := c.keys[env.KeyID]
	if !ok {
		return "", ErrUnauthorized
	}
	sealed, err := base64.StdEncoding.DecodeString(env.Ciphertext)
	if err != nil || len(sealed) < aead.NonceSize()+aead.Overhead() {
		return "", ErrUnauthorized
	}
	env.Ciphertext = ""
	aad, err := json.Marshal(env)
	if err != nil {
		return "", ErrUnauthorized
	}
	nonce := sealed[:aead.NonceSize()]
	plaintext, err := aead.Open(nil, nonce, sealed[aead.NonceSize():], aad)
	if err != nil {
		return "", ErrUnauthorized
	}
	return string(plaintext), nil
}

// SealRequest captures both credentials for a delegated caller. Plaintext never enters the domain model.
func (c *SessionCipher) SealRequest(_ context.Context, r *http.Request, p domain.Principal) (json.RawMessage, error) {
	token, err := bearer(r.Header.Get("Authorization"))
	if err != nil {
		return nil, err
	}
	subject := ""
	if p.Actor != p.Subject {
		subject = r.Header.Get("X-Subject-Token")
		if subject == "" {
			return nil, ErrUnauthorized
		}
	}
	bundle, err := json.Marshal([]string{token, subject})
	if err != nil {
		return nil, err
	}
	return c.Seal(p, string(bundle))
}

// VerifySession rechecks live access tokens. A service session also requires a current deployment registration check.
func (a *Authenticator) VerifySession(
	ctx context.Context,
	c *SessionCipher,
	expected domain.Principal,
	raw json.RawMessage,
	checkService func(context.Context, domain.Principal) (Binding, error),
) (domain.Principal, string, error) {
	plain, err := c.Open(expected, raw)
	if err != nil {
		return domain.Principal{}, "", err
	}
	var tokens []string
	if json.Unmarshal([]byte(plain), &tokens) != nil || len(tokens) != 2 {
		return domain.Principal{}, "", ErrUnauthorized
	}
	userToken := tokens[0]
	var actor domain.Principal
	var binding Binding
	if expected.Actor != expected.Subject {
		actor, binding, err = a.verifySessionActor(ctx, expected, tokens[0], checkService)
		if err != nil {
			return domain.Principal{}, "", err
		}
		userToken = tokens[1]
	}
	user, err := a.authenticateToken(ctx, userToken)
	if err != nil {
		return domain.Principal{}, "", err
	}
	userService, err := a.classifyService(ctx, user)
	if err != nil {
		return domain.Principal{}, "", err
	}
	if userService || user.Issuer != expected.Issuer || user.Subject != expected.Subject ||
		user.OrgID != expected.OrgID {
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

func (a *Authenticator) verifySessionActor(
	ctx context.Context,
	expected domain.Principal,
	token string,
	checkService func(context.Context, domain.Principal) (Binding, error),
) (domain.Principal, Binding, error) {
	if checkService == nil {
		return domain.Principal{}, Binding{}, ErrUnauthorized
	}
	actor, err := a.authenticateToken(ctx, token)
	if err != nil {
		return domain.Principal{}, Binding{}, err
	}
	actorService, err := a.classifyService(ctx, actor)
	if err != nil {
		return domain.Principal{}, Binding{}, err
	}
	if actor.Subject != expected.Actor || actor.OrgID != expected.OrgID || !actorService {
		return domain.Principal{}, Binding{}, ErrUnauthorized
	}
	binding, err := checkService(ctx, actor)
	if err != nil || binding.Actor != actor.Subject {
		return domain.Principal{}, Binding{}, ErrUnauthorized
	}
	return actor, binding, nil
}

// SessionResolver loads only a session bound to the independently verified current actor.
type SessionResolver func(context.Context, string, domain.Principal) (domain.Principal, json.RawMessage, error)

// UseSessions configures persisted delegation before the authenticator serves requests.
func (a *Authenticator) UseSessions(
	c *SessionCipher,
	resolver SessionResolver,
	checkService func(context.Context, domain.Principal) (Binding, error),
) error {
	if c == nil || resolver == nil || checkService == nil {
		return errors.New("session cipher, resolver and service registration check required")
	}
	a.sessionCipher = c
	a.sessionResolver = resolver
	a.sessionServiceCheck = checkService
	return nil
}

func (a *Authenticator) authenticateSession(
	ctx context.Context,
	r *http.Request,
	actor domain.Principal,
	id string,
) (domain.Principal, error) {
	if a.sessionCipher == nil || a.sessionResolver == nil || a.binding == nil || len(id) > 256 {
		return domain.Principal{}, ErrUnauthorized
	}
	binding, err := a.binding(ctx, r, actor)
	if err != nil || binding.Actor != actor.Subject {
		return domain.Principal{}, ErrUnauthorized
	}
	expected, envelope, err := a.sessionResolver(ctx, id, actor)
	if err != nil {
		return domain.Principal{}, err
	}
	if expected.Actor != actor.Subject || expected.Issuer != actor.Issuer || expected.OrgID != actor.OrgID {
		return domain.Principal{}, ErrUnauthorized
	}
	fresh, _, err := a.VerifySession(ctx, a.sessionCipher, expected, envelope, a.sessionServiceCheck)
	if err != nil {
		return domain.Principal{}, err
	}
	fresh.Permissions = delegatedPermissions(fresh.Permissions, actor.Permissions, binding.Permissions)
	if actor.ExpiresAt.Before(fresh.ExpiresAt) {
		fresh.ExpiresAt = actor.ExpiresAt
	}
	return fresh, nil
}
