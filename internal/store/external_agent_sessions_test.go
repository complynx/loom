package store_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
)

func TestExternalAgentSessionsKeepOwnershipAndRevokeIndependently(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ctx := t.Context()
	owner := principal("mcp-owner")
	confirmer := owner
	confirmer.Actor = "chat-client"
	confirmer.Permissions = map[string]bool{domain.ExternalAgentProfilePermission: true}
	profile, err := s.PutExternalAgentProfile(ctx, confirmer, domain.ExternalAgentProfileUpdate{Enabled: true})
	require.NoError(t, err)

	caller := domain.ExternalCaller{Principal: owner, ClientID: "agent"}
	caller.SessionID, err = s.OpenExternalAgentSession(ctx, caller)
	require.NoError(t, err)
	initial, err := s.ListExternalAgentSessions(ctx, owner)
	require.NoError(t, err)
	require.Len(t, initial, 1)
	reference := initial[0].ID
	require.NotEqual(t, caller.SessionID, reference)
	otherSession := caller
	otherSession.SessionID, err = s.OpenExternalAgentSession(ctx, caller)
	require.NoError(t, err)
	require.NotEqual(t, caller.SessionID, otherSession.SessionID)
	sessions, err := s.ListExternalAgentSessions(ctx, owner)
	require.NoError(t, err)
	require.Len(t, sessions, 2)

	foreign := caller
	foreign.ClientID = "other-agent"
	require.ErrorIs(t, s.CheckExternalAgentSession(ctx, foreign), domain.ErrNotFound)
	require.ErrorIs(t, s.CloseExternalAgentSession(ctx, foreign), domain.ErrNotFound)
	foreign = caller
	foreign.SessionID = reference
	require.ErrorIs(t, s.CheckExternalAgentSession(ctx, foreign), domain.ErrNotFound)
	require.ErrorIs(t, s.CloseExternalAgentSession(ctx, foreign), domain.ErrNotFound)
	foreign.Principal = principal("mcp-outsider")
	require.ErrorIs(t, s.RevokeExternalAgentSession(ctx, foreign.Principal, reference), domain.ErrNotFound)
	require.NoError(t, s.CheckExternalAgentSession(ctx, caller))

	require.NoError(t, s.RevokeExternalAgentSession(ctx, owner, reference))
	require.ErrorIs(t, s.CheckExternalAgentSession(ctx, caller), domain.ErrNotFound)
	require.NoError(t, s.CheckExternalAgentSession(ctx, otherSession))

	require.NoError(t, s.DisableExternalAgentAccess(ctx, owner))
	require.ErrorIs(t, s.CheckExternalAgentSession(ctx, otherSession), domain.ErrNotFound)
	_, err = s.OpenExternalAgentSession(ctx, caller)
	require.ErrorContains(t, err, "external_agent_disabled")
	disabled, err := s.ExternalAgentProfile(ctx, owner)
	require.NoError(t, err)
	require.False(t, disabled.Enabled)
	require.Equal(t, profile.Version+1, disabled.Version)
	_, err = s.PutExternalAgentProfile(
		ctx,
		confirmer,
		domain.ExternalAgentProfileUpdate{Enabled: true, Version: disabled.Version},
	)
	require.NoError(t, err)
	require.ErrorIs(t, s.CheckExternalAgentSession(ctx, otherSession), domain.ErrNotFound)
	_, err = s.OpenExternalAgentSession(ctx, caller)
	require.NoError(t, err)
}

func TestExternalAgentSessionOverflowClosesOldestConnection(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ctx := t.Context()
	owner := principal("mcp-reconnect")
	confirmer := owner
	confirmer.Actor = "chat-client"
	confirmer.Permissions = map[string]bool{domain.ExternalAgentProfilePermission: true}
	_, err := s.PutExternalAgentProfile(ctx, confirmer, domain.ExternalAgentProfileUpdate{Enabled: true})
	require.NoError(t, err)
	caller := domain.ExternalCaller{Principal: owner, ClientID: "agent"}
	first, err := s.OpenExternalAgentSession(ctx, caller)
	require.NoError(t, err)
	for range 16 {
		caller.SessionID, err = s.OpenExternalAgentSession(ctx, caller)
		require.NoError(t, err)
	}
	require.NoError(t, s.CheckExternalAgentSession(ctx, caller))
	caller.SessionID = first
	require.ErrorIs(t, s.CheckExternalAgentSession(ctx, caller), domain.ErrNotFound)
	sessions, err := s.ListExternalAgentSessions(ctx, owner)
	require.NoError(t, err)
	require.Len(t, sessions, 16)
}
