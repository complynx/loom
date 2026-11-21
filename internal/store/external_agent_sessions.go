package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
)

const maxExternalAgentSessions = 16

func (s *Store) OpenExternalAgentSession(ctx context.Context, caller domain.ExternalCaller) (string, error) {
	p := caller.Principal
	if p.ServiceAccount || p.Issuer == "" || p.Subject == "" || p.OrgID == "" || caller.ClientID == "" {
		return "", domain.Forbidden()
	}
	transportID := uuid.NewString()
	err := s.tx(ctx, func(tx pgx.Tx) error {
		// Serialize admission with other opens and with profile changes.
		var owner string
		err := tx.QueryRow(ctx, `SELECT p.owner_id FROM external_agent_profiles p
			JOIN users u ON u.id=p.owner_id
			WHERE u.issuer=$1 AND u.subject=$2 AND p.org_id=$3 AND p.enabled
			FOR UPDATE OF p`, p.Issuer, p.Subject, p.OrgID).Scan(&owner)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NewError(http.StatusForbidden, "external_agent_disabled")
		}
		if err != nil {
			return err
		}
		if err = evictExternalAgentSession(ctx, tx, p, owner); err != nil {
			return err
		}
		// Bound cleanup work for an active installation.
		_, err = tx.Exec(ctx, `DELETE FROM external_agent_sessions WHERE id IN (
			SELECT id FROM external_agent_sessions WHERE expires_at<=now()
			ORDER BY expires_at LIMIT 32 FOR UPDATE SKIP LOCKED)`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO external_agent_sessions(id,transport_id,owner_id,org_id,client_id)
			VALUES($1,$2,$3,$4,$5)`, uuid.NewString(), transportID, owner, p.OrgID, caller.ClientID)
		return err
	})
	if err != nil {
		return "", err
	}
	return transportID, nil
}

func evictExternalAgentSession(ctx context.Context, tx pgx.Tx, p domain.Principal, owner string) error {
	var id string
	err := tx.QueryRow(ctx, `DELETE FROM external_agent_sessions WHERE id=(
		SELECT id FROM external_agent_sessions WHERE owner_id=$1 AND org_id=$2 AND expires_at>now()
		ORDER BY created_at DESC,id DESC OFFSET $3 LIMIT 1
	) RETURNING id`, owner, p.OrgID, maxExternalAgentSessions-1).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return audit(ctx, tx, p, "external_agent.session.evict", id, nil)
}

// CheckExternalAgentSession binds the transport ID to the issuer, user, organization and OAuth client.
func (s *Store) CheckExternalAgentSession(ctx context.Context, caller domain.ExternalCaller) error {
	_, _, err := s.ExternalAgentSessionOwner(ctx, caller)
	return err
}

// ExternalAgentSessionOwner resolves the durable owner and session IDs.
func (s *Store) ExternalAgentSessionOwner(ctx context.Context, caller domain.ExternalCaller) (string, string, error) {
	p := caller.Principal
	if _, err := uuid.Parse(caller.SessionID); err != nil || p.ServiceAccount {
		return "", "", domain.NotFound()
	}
	var id, owner string
	err := s.pool.QueryRow(ctx, `SELECT x.id,x.owner_id FROM external_agent_sessions x JOIN users u ON u.id=x.owner_id
		WHERE x.transport_id=$1 AND u.issuer=$2 AND u.subject=$3 AND x.org_id=$4
		AND x.client_id=$5 AND x.expires_at>now()`,
		caller.SessionID, p.Issuer, p.Subject, p.OrgID, caller.ClientID).Scan(&id, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", domain.NotFound()
	}
	return id, owner, err
}

func (s *Store) ListExternalAgentSessions(
	ctx context.Context,
	p domain.Principal,
) ([]domain.ExternalAgentSession, error) {
	if p.ServiceAccount || !p.Has("chat.read") {
		return nil, domain.Forbidden()
	}
	rows, err := s.pool.Query(ctx, `SELECT x.id,x.client_id,x.created_at,x.expires_at
		FROM external_agent_sessions x JOIN users u ON u.id=x.owner_id
		WHERE u.issuer=$1 AND u.subject=$2 AND x.org_id=$3 AND x.expires_at>now()
		ORDER BY x.created_at DESC,x.id`, p.Issuer, p.Subject, p.OrgID)
	if err != nil {
		return nil, err
	}
	out := []domain.ExternalAgentSession{}
	defer rows.Close()
	for rows.Next() {
		var session domain.ExternalAgentSession
		if err = rows.Scan(&session.ID, &session.ClientID, &session.CreatedAt, &session.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, session)
	}
	return out, rows.Err()
}

// CloseExternalAgentSession closes the caller's own MCP connection. Client identity
// is part of the deletion condition so a concurrent request cannot change its scope.
func (s *Store) CloseExternalAgentSession(ctx context.Context, caller domain.ExternalCaller) error {
	return s.deleteExternalAgentSession(ctx, caller.Principal, caller.SessionID, &caller.ClientID)
}

func (s *Store) RevokeExternalAgentSession(ctx context.Context, p domain.Principal, id string) error {
	if !p.Has("chat.read") {
		return domain.Forbidden()
	}
	return s.deleteExternalAgentSession(ctx, p, id, nil)
}

// DisableExternalAgentAccess revokes all current sessions and requires a new
// profile confirmation before any external agent can open another one.
func (s *Store) DisableExternalAgentAccess(ctx context.Context, p domain.Principal) error {
	if p.ServiceAccount || !p.Has("chat.read") {
		return domain.Forbidden()
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		var owner string
		err := tx.QueryRow(ctx, `UPDATE external_agent_profiles p
			SET enabled=false,version=p.version+1,updated_at=now() FROM users u
			WHERE p.owner_id=u.id AND u.issuer=$1 AND u.subject=$2 AND p.org_id=$3 AND p.enabled
			RETURNING p.owner_id`, p.Issuer, p.Subject, p.OrgID).Scan(&owner)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err = tx.Exec(
			ctx,
			"DELETE FROM external_agent_sessions WHERE owner_id=$1 AND org_id=$2",
			owner,
			p.OrgID,
		); err != nil {
			return err
		}
		return audit(ctx, tx, p, "external_agent.disable", owner, nil)
	})
}

func (s *Store) deleteExternalAgentSession(ctx context.Context, p domain.Principal, id string, client *string) error {
	if _, err := uuid.Parse(id); err != nil || p.ServiceAccount {
		return domain.NotFound()
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		var reference string
		err := tx.QueryRow(ctx, `DELETE FROM external_agent_sessions x USING users u
			WHERE u.id=x.owner_id AND u.issuer=$2 AND u.subject=$3 AND x.org_id=$4 AND x.expires_at>now()
			AND (($5::text IS NULL AND x.id=$1) OR (x.transport_id=$1 AND x.client_id=$5))
			RETURNING x.id`, id, p.Issuer, p.Subject, p.OrgID, client).Scan(&reference)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound()
		}
		if err != nil {
			return err
		}
		return audit(ctx, tx, p, "external_agent.session.revoke", reference, nil)
	})
}
