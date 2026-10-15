package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/identity"
)

func TestSessionRotationResumesAndPreservesAuthorization(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	ctx := context.Background()
	oldKey := make([]byte, 32)
	newKey := make([]byte, 32)
	newKey[0] = 1
	old, err := identity.NewSessionKeyring("old", map[string][]byte{"old": oldKey}, "aud", "client")
	require.NoError(t, err)
	current, err := identity.NewSessionKeyring("new", map[string][]byte{"old": oldKey, "new": newKey}, "aud", "client")
	require.NoError(t, err)
	p := domain.Principal{
		Issuer:    "issuer",
		Subject:   "user",
		Actor:     "user",
		OrgID:     "org",
		ExpiresAt: time.Now().Add(time.Hour).Truncate(time.Second),
	}
	raw, err := old.Seal(p, "secret")
	require.NoError(t, err)
	sessions := make([]domain.AuthSession, 3)
	for i := range sessions {
		sessions[i], err = s.CreateAuthSession(ctx, p, raw, p.ExpiresAt)
		require.NoError(t, err)
	}
	require.NoError(t, s.RevokeAuthSession(ctx, p, sessions[0].ID))
	require.NoError(t, s.CheckSessionKeys(ctx, current.HasKey))
	onlyNew, err := identity.NewSessionKeyring("new", map[string][]byte{"new": newKey}, "aud", "client")
	require.NoError(t, err)
	require.Error(t, s.CheckSessionKeys(ctx, onlyNew.HasKey))
	rewrap := func(v domain.AuthSession) ([]byte, error) {
		return current.Rewrap(
			domain.Principal{
				Issuer:    v.Issuer,
				Subject:   v.Subject,
				Actor:     v.Actor,
				OrgID:     v.OrgID,
				ExpiresAt: v.ExpiresAt,
			},
			v.Envelope,
		)
	}
	n, err := s.RotateSessionKeys(ctx, "new", 1, rewrap)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	// An interruption after touching a record must roll back the entire next batch.
	cancelled, cancel := context.WithCancel(ctx)
	n, err = s.RotateSessionKeys(
		cancelled,
		"new",
		2,
		func(v domain.AuthSession) ([]byte, error) { cancel(); return rewrap(v) },
	)
	require.Error(t, err)
	require.Zero(t, n)
	require.Error(t, s.CheckSessionKeys(ctx, onlyNew.HasKey))
	// Restart the migrator using the same keyring and complete remaining records.
	n, err = s.RotateSessionKeys(ctx, "new", 100, rewrap)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	n, err = s.RotateSessionKeys(ctx, "new", 100, rewrap)
	require.NoError(t, err)
	require.Zero(t, n)
	require.NoError(t, s.CheckSessionKeys(ctx, onlyNew.HasKey))
	for i, before := range sessions {
		after, sessionErr := s.LoadAuthSession(ctx, p, before.ID)
		require.NoError(t, sessionErr)
		token, sessionErr := onlyNew.Open(p, after.Envelope)
		require.NoError(t, sessionErr)
		require.Equal(t, "secret", token)
		require.Equal(t, before.ExpiresAt, after.ExpiresAt)
		if i == 0 {
			require.NotNil(t, after.RevokedAt)
			require.Equal(t, int64(2), after.Version)
		} else {
			require.Nil(t, after.RevokedAt)
			require.Equal(t, int64(1), after.Version)
		}
	}
}

func TestSessionRotationRejectsCorruptionAtomically(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	ctx := context.Background()
	p := domain.Principal{Issuer: "issuer", Subject: "user", Actor: "user", ExpiresAt: time.Now().Add(time.Hour)}
	for range 2 {
		_, err := s.CreateAuthSession(ctx, p, []byte(`{"key_id":"old"}`), p.ExpiresAt)
		require.NoError(t, err)
	}
	calls := 0
	n, err := s.RotateSessionKeys(ctx, "new", 2, func(domain.AuthSession) ([]byte, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("corrupt envelope")
		}
		return []byte(`{"key_id":"new"}`), nil
	})
	require.Error(t, err)
	require.Zero(t, n)
	n, err = s.RotateSessionKeys(
		ctx,
		"new",
		2,
		func(domain.AuthSession) ([]byte, error) { return []byte(`{"key_id":"new"}`), nil },
	)
	require.NoError(t, err)
	require.Equal(t, 2, n)
}

func TestSessionRotationDoesNotOverwriteConcurrentReauthorization(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	ctx := context.Background()
	oldKey := make([]byte, 32)
	newKey := make([]byte, 32)
	newKey[0] = 1
	old, err := identity.NewSessionKeyring("old", map[string][]byte{"old": oldKey}, "aud", "client")
	require.NoError(t, err)
	current, err := identity.NewSessionKeyring("new", map[string][]byte{"old": oldKey, "new": newKey}, "aud", "client")
	require.NoError(t, err)
	p := domain.Principal{
		Issuer:    "issuer",
		Subject:   "user",
		Actor:     "user",
		ExpiresAt: time.Now().Add(time.Hour).Truncate(time.Second),
	}
	raw, err := old.Seal(p, "old-token")
	require.NoError(t, err)
	session, err := s.CreateAuthSession(ctx, p, raw, p.ExpiresAt)
	require.NoError(t, err)
	fresh, err := current.Seal(p, "fresh-token")
	require.NoError(t, err)
	locked := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, rotationErr := s.RotateSessionKeys(ctx, "new", 1, func(v domain.AuthSession) ([]byte, error) {
			close(locked)
			<-release
			return current.Rewrap(p, v.Envelope)
		})
		done <- rotationErr
	}()
	<-locked
	// Reauthorization is allowed to wait behind rotation, then must win unchanged.
	refreshed := make(chan error, 1)
	go func() {
		_, reauthErr := s.ReauthorizeAuthSession(ctx, p, session.ID, fresh, p.ExpiresAt, session.Version)
		refreshed <- reauthErr
	}()
	close(release)
	require.NoError(t, <-done)
	require.NoError(t, <-refreshed)
	saved, err := s.LoadAuthSession(ctx, p, session.ID)
	require.NoError(t, err)
	token, err := current.Open(p, saved.Envelope)
	require.NoError(t, err)
	require.Equal(t, "fresh-token", token)
	require.Equal(t, int64(2), saved.Version)
}
