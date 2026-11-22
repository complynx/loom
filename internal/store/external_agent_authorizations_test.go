package store_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
)

func TestExternalAgentAuthorizationRevocationClosesSessionsAndPreventsReconnect(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ctx := t.Context()
	owner, other := principal("mcp-owner"), principal("mcp-other")
	confirmer := owner
	confirmer.Actor = "chat-client"
	confirmer.Permissions = map[string]bool{domain.ExternalAgentProfilePermission: true}
	profile, err := s.PutExternalAgentProfile(ctx, confirmer, domain.ExternalAgentProfileUpdate{Enabled: true})
	require.NoError(t, err)
	expires := time.Now().Add(time.Hour)
	register := func(p domain.Principal, expiresAt time.Time) domain.ExternalCaller {
		id := uuid.NewString()
		require.NoError(t, s.CreateExternalAgentAuthorization(ctx, p, id, "same-agent", "Local client", expiresAt))
		return domain.ExternalCaller{Principal: p, ClientID: "same-agent", AuthorizationID: id}
	}
	first, second, third := register(owner, expires), register(owner, expires), register(owner, expires)
	foreign := register(other, expires)
	expired := register(owner, time.Now().Add(-time.Minute))
	list, err := s.ListExternalAgentAuthorizations(ctx, owner)
	require.NoError(t, err)
	require.Len(t, list, 3)
	require.Equal(t, third.AuthorizationID, list[0].ID)
	require.Equal(t, "Local client", list[0].Name)
	active, err := s.HasExternalAgentAuthorization(ctx, expired)
	require.NoError(t, err)
	require.False(t, active)
	first.SessionID, err = s.OpenExternalAgentSession(ctx, first)
	require.NoError(t, err)
	second.SessionID, err = s.OpenExternalAgentSession(ctx, second)
	require.NoError(t, err)
	mismatched := second
	mismatched.SessionID = first.SessionID
	require.ErrorIs(t, s.CheckExternalAgentSession(ctx, mismatched), domain.ErrNotFound)
	require.ErrorIs(t, s.CloseExternalAgentSession(ctx, mismatched), domain.ErrNotFound)
	ids := []string{first.AuthorizationID, third.AuthorizationID, foreign.AuthorizationID}
	require.NoError(t, s.RevokeExternalAgentAuthorizations(ctx, confirmer, ids))
	require.NoError(t, s.RevokeExternalAgentAuthorizations(ctx, confirmer, ids))
	active, err = s.HasExternalAgentAuthorization(ctx, first)
	require.NoError(t, err)
	require.False(t, active)
	require.ErrorIs(t, s.CheckExternalAgentSession(ctx, first), domain.ErrNotFound)
	_, err = s.OpenExternalAgentSession(ctx, first)
	require.ErrorContains(t, err, "unauthorized")
	require.NoError(t, s.CheckExternalAgentSession(ctx, second))
	active, err = s.HasExternalAgentAuthorization(ctx, foreign)
	require.NoError(t, err)
	require.True(t, active)
	unchanged, err := s.ExternalAgentProfile(ctx, owner)
	require.NoError(t, err)
	require.Equal(t, profile, unchanged)
	_, err = s.PutExternalAgentProfile(ctx, confirmer, domain.ExternalAgentProfileUpdate{Version: profile.Version})
	require.NoError(t, err)
	list, err = s.ListExternalAgentAuthorizations(ctx, owner)
	require.NoError(t, err)
	require.Empty(t, list)
	require.ErrorIs(t, s.CheckExternalAgentSession(ctx, second), domain.ErrNotFound)
}

func TestExternalAgentAuthorizationOverflowRevokesOldestGrant(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ctx := t.Context()
	owner := principal("mcp-owner")
	confirmer := owner
	confirmer.Actor = "chat-client"
	confirmer.Permissions = map[string]bool{domain.ExternalAgentProfilePermission: true}
	_, err := s.PutExternalAgentProfile(ctx, confirmer, domain.ExternalAgentProfileUpdate{Enabled: true})
	require.NoError(t, err)
	const clientID = "local-agent"
	expires := time.Now().Add(time.Hour)
	oldest := domain.ExternalCaller{Principal: owner, ClientID: clientID, AuthorizationID: uuid.NewString()}
	require.NoError(
		t,
		s.CreateExternalAgentAuthorization(ctx, owner, oldest.AuthorizationID, clientID, "Local", expires),
	)
	oldest.SessionID, err = s.OpenExternalAgentSession(ctx, oldest)
	require.NoError(t, err)
	for range domain.MaxExternalAgentAuthorizations - 1 {
		require.NoError(t, s.CreateExternalAgentAuthorization(ctx, owner, uuid.NewString(), clientID, "Local", expires))
	}
	require.NoError(t, s.CheckExternalAgentSession(ctx, oldest))
	newest := uuid.NewString()
	require.NoError(t, s.CreateExternalAgentAuthorization(ctx, owner, newest, clientID, "Local", expires))
	list, err := s.ListExternalAgentAuthorizations(ctx, owner)
	require.NoError(t, err)
	require.Len(t, list, domain.MaxExternalAgentAuthorizations)
	require.Equal(t, newest, list[0].ID)
	active, err := s.HasExternalAgentAuthorization(ctx, oldest)
	require.NoError(t, err)
	require.False(t, active)
	require.ErrorIs(t, s.CheckExternalAgentSession(ctx, oldest), domain.ErrNotFound)
}
