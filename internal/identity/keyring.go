package identity

import (
	"encoding/json"
	"errors"

	"github.com/complynx/loom/internal/domain"
)

// NewSessionKeyring uses the active key for writes and all supplied keys for reads.
// Key IDs must never be reused for different key material.
func NewSessionKeyring(active string, keys map[string][]byte, audience, clientID string) (*SessionCipher, error) {
	c, err := NewSessionCipherForClient(active, keys[active], audience, clientID)
	if err != nil {
		return nil, err
	}
	for id, key := range keys {
		other, newSessionCipherErr := NewSessionCipher(id, key)
		if newSessionCipherErr != nil {
			return nil, newSessionCipherErr
		}
		c.keys[id] = other.aead
	}
	return c, nil
}

func (c *SessionCipher) HasKey(id string) bool {
	_, ok := c.keys[id]
	return ok
}

func (c *SessionCipher) ActiveKeyID() string { return c.keyID }

// Rewrap authenticates the original binding and preserves expiry. It can migrate
// expired or revoked records but does not make them usable for authentication.
func (c *SessionCipher) Rewrap(expected domain.Principal, raw json.RawMessage) (json.RawMessage, error) {
	token, err := c.open(expected, raw, true)
	if err != nil {
		return nil, errors.New("session envelope cannot be authenticated for rotation")
	}
	return c.seal(expected, token, true)
}
