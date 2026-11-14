package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
)

const externalAgentProfileColumns = "enabled,history,memory,version,confirmed_by,updated_at"

// ExternalAgentProfile returns the stored consent of the authenticated user.
// A user without a stored profile gets the zero profile: all access is denied.
// It is one read without side effects: it runs before every external module call.
func (s *Store) ExternalAgentProfile(ctx context.Context, p domain.Principal) (domain.ExternalAgentProfile, error) {
	out := domain.ExternalAgentProfile{}
	if p.ServiceAccount || p.Issuer == "" || p.Subject == "" || p.OrgID == "" {
		return out, domain.Forbidden()
	}
	err := s.pool.QueryRow(
		ctx,
		"SELECT p.enabled,p.history,p.memory,p.version,p.confirmed_by,p.updated_at FROM external_agent_profiles p JOIN users u ON u.id=p.owner_id WHERE u.issuer=$1 AND u.subject=$2 AND p.org_id=$3",
		p.Issuer,
		p.Subject,
		p.OrgID,
	).Scan(&out.Enabled, &out.History, &out.Memory, &out.Version, &out.ConfirmedBy, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ExternalAgentProfile{}, nil
	}
	return out, err
}

// PutExternalAgentProfile replaces the profile and records the confirming actor.
// Version zero creates the profile. A stale version returns a conflict.
func (s *Store) PutExternalAgentProfile(
	ctx context.Context,
	p domain.Principal,
	in domain.ExternalAgentProfileUpdate,
) (domain.ExternalAgentProfile, error) {
	out := domain.ExternalAgentProfile{}
	// Only a confirming caller that acts for the user writes the profile. A user's
	// direct token is refused even if a role grants the permission.
	if !p.Has(domain.ExternalAgentProfilePermission) || p.ServiceAccount || p.Actor == "" || p.Actor == p.Subject {
		return out, domain.Forbidden()
	}
	if in.Version < 0 {
		return out, domain.Conflict("external_agent_profile_version_conflict")
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		uid, e := s.ensureUser(ctx, tx, p)
		if e != nil {
			return e
		}
		query := "UPDATE external_agent_profiles SET enabled=$3,history=$4,memory=$5,confirmed_by=$6,version=version+1,updated_at=now() WHERE owner_id=$1 AND org_id=$2 AND version=$7 RETURNING " + externalAgentProfileColumns
		if in.Version == 0 {
			query = "INSERT INTO external_agent_profiles(owner_id,org_id,enabled,history,memory,confirmed_by,version) VALUES($1,$2,$3,$4,$5,$6,$7+1) ON CONFLICT DO NOTHING RETURNING " + externalAgentProfileColumns
		}
		e = tx.QueryRow(ctx, query, uid, p.OrgID, in.Enabled, in.History, in.Memory, actor(p), in.Version).
			Scan(&out.Enabled, &out.History, &out.Memory, &out.Version, &out.ConfirmedBy, &out.UpdatedAt)
		if errors.Is(e, pgx.ErrNoRows) {
			return domain.Conflict("external_agent_profile_version_conflict")
		}
		if e != nil {
			return e
		}
		return audit(ctx, tx, p, "external_agent.profile", uid, in)
	})
	return out, err
}

// AppendExternalAction writes one external agent action to the user's default chat.
// It is a passive event: it never starts or wakes a run. The action ID makes a
// repeated write of the same action return the first event.
func (s *Store) AppendExternalAction(
	ctx context.Context,
	p domain.Principal,
	action domain.ExternalAction,
	text string,
) (domain.ChatEvent, error) {
	out := domain.ChatEvent{}
	if p.ServiceAccount || action.ID == "" || action.ClientID == "" || text == "" {
		return out, domain.Forbidden()
	}
	source := p
	source.Actor = domain.ExternalAgentActorPrefix + action.ClientID
	in := domain.AppendEvent{
		SourceEventID: action.ID,
		Type:          domain.ExternalAgentActionEvent,
		Action:        action.Tool,
		Text:          text,
		Data:          document(action),
	}
	requestHash := hash(in)
	err := s.tx(ctx, func(tx pgx.Tx) error {
		return s.appendEvent(ctx, tx, source, defaultChatAlias, in, requestHash, &out)
	})
	return out, err
}
