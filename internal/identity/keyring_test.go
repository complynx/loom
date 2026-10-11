// Internal sealing creates an expired persisted credential without changing the clock.
//
//nolint:testpackage // The expired-session test requires the private sealing path.
package identity

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/clx-scripter/internal/domain"
)

func TestSessionKeyRotationAcrossRestart(t *testing.T) {
	t.Parallel()
	oldKey := make([]byte, 32)
	newKey := make([]byte, 32)
	newKey[0] = 1
	p := domain.Principal{
		Issuer:    "issuer",
		Subject:   "user",
		Actor:     "service",
		OrgID:     "org",
		ExpiresAt: time.Now().Add(time.Hour).Truncate(time.Second),
	}
	old, err := NewSessionKeyring(
		"old",
		map[string][]byte{"old": oldKey, "new": newKey},
		"aud",
		"client",
	)
	require.NoError(t, err)
	oldEnvelope, err := old.Seal(p, "secret")
	require.NoError(t, err)
	current, err := NewSessionKeyring(
		"new",
		map[string][]byte{"old": oldKey, "new": newKey},
		"aud",
		"client",
	)
	require.NoError(t, err)
	token, err := current.Open(p, oldEnvelope)
	require.NoError(t, err)
	require.Equal(t, "secret", token)
	newEnvelope, err := current.Seal(p, "new-secret")
	require.NoError(t, err)
	token, err = old.Open(p, newEnvelope)
	require.NoError(t, err)
	require.Equal(t, "new-secret", token)
	migrated, err := current.Rewrap(p, oldEnvelope)
	require.NoError(t, err)
	restarted, err := NewSessionKeyring("new", map[string][]byte{"new": newKey}, "aud", "client")
	require.NoError(t, err)
	token, err = restarted.Open(p, migrated)
	require.NoError(t, err)
	require.Equal(t, "secret", token)
	_, err = restarted.Open(p, oldEnvelope)
	require.ErrorIs(t, err, ErrUnauthorized)
	for _, change := range []string{"subject", "actor", "org_id", "client_id", "key_id", "expires_at"} {
		var env map[string]any
		require.NoError(t, json.Unmarshal(migrated, &env))
		if change == "expires_at" {
			env[change] = float64(p.ExpiresAt.Unix() + 1)
		} else {
			env[change] = "changed"
		}
		tampered, marshalErr := json.Marshal(env)
		require.NoError(t, marshalErr)
		_, marshalErr = current.Rewrap(p, tampered)
		require.Error(t, marshalErr, change)
	}
}

func TestRotationPreservesExpiredSessionWithoutAuthorizingIt(t *testing.T) {
	t.Parallel()
	c, err := NewSessionKeyring("key", map[string][]byte{"key": make([]byte, 32)}, "aud", "client")
	require.NoError(t, err)
	p := domain.Principal{
		Issuer:    "issuer",
		Subject:   "user",
		Actor:     "user",
		ExpiresAt: time.Now().Add(-time.Hour).Truncate(time.Second),
	}
	// The internal sealing path represents a previously valid stored credential.
	raw, err := c.seal(p, "expired", true)
	require.NoError(t, err)
	migrated, err := c.Rewrap(p, raw)
	require.NoError(t, err)
	_, err = c.Open(p, migrated)
	require.ErrorIs(t, err, ErrUnauthorized)
	_, err = c.Seal(p, "expired")
	require.ErrorIs(t, err, ErrUnauthorized)
}
