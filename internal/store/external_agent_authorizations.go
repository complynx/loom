package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
)

// CreateExternalAgentAuthorization records a broker login before its token can be used.
func (s *Store) CreateExternalAgentAuthorization(
	ctx context.Context, p domain.Principal, id, clientID, name string, expiresAt time.Time,
) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		// The user upsert locks this owner through the cap check and insert.
		owner, err := s.ensureUser(ctx, tx, p)
		if err != nil {
			return err
		}
		// Bound cleanup work, and keep the newest authorizations for this owner.
		_, err = tx.Exec(ctx, `DELETE FROM external_agent_authorizations WHERE id IN (
			SELECT id FROM external_agent_authorizations WHERE expires_at<=now()
			ORDER BY expires_at LIMIT 32 FOR UPDATE SKIP LOCKED)`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM external_agent_authorizations WHERE id IN (
			SELECT id FROM external_agent_authorizations WHERE owner_id=$1 AND org_id=$2 AND expires_at>now()
			ORDER BY created_at DESC,id DESC OFFSET $3)`, owner, p.OrgID, domain.MaxExternalAgentAuthorizations-1)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO external_agent_authorizations
			(id,owner_id,org_id,client_id,client_name,expires_at)
			VALUES($1,$2,$3,$4,$5,$6)`, id, owner, p.OrgID, clientID, name, expiresAt)
		return err
	})
}

func (s *Store) HasExternalAgentAuthorization(ctx context.Context, caller domain.ExternalCaller) (bool, error) {
	p := caller.Principal
	var active bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM external_agent_authorizations x
		JOIN users u ON u.id=x.owner_id WHERE x.id=$1 AND u.issuer=$2 AND u.subject=$3 AND x.org_id=$4
		AND x.client_id=$5 AND x.expires_at>now())`,
		caller.AuthorizationID, p.Issuer, p.Subject, p.OrgID, caller.ClientID).Scan(&active)
	return active, err
}

func (s *Store) ListExternalAgentAuthorizations(
	ctx context.Context, p domain.Principal,
) ([]domain.ExternalAgentAuthorization, error) {
	rows, err := s.pool.Query(ctx, `SELECT x.id,x.client_name,x.created_at,x.expires_at
		FROM external_agent_authorizations x JOIN users u ON u.id=x.owner_id
		WHERE u.issuer=$1 AND u.subject=$2 AND x.org_id=$3 AND x.expires_at>now()
		ORDER BY x.created_at DESC,x.id`, p.Issuer, p.Subject, p.OrgID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.ExternalAgentAuthorization, error) {
		var grant domain.ExternalAgentAuthorization
		scanErr := row.Scan(&grant.ID, &grant.Name, &grant.CreatedAt, &grant.ExpiresAt)
		return grant, scanErr
	})
}

// RevokeExternalAgentAuthorizations removes only the selected grants the user owns.
func (s *Store) RevokeExternalAgentAuthorizations(ctx context.Context, p domain.Principal, ids []string) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `DELETE FROM external_agent_authorizations x USING users u
			WHERE u.id=x.owner_id AND u.issuer=$1 AND u.subject=$2 AND x.org_id=$3 AND x.id=ANY($4::uuid[])
			RETURNING x.id`, p.Issuer, p.Subject, p.OrgID, ids)
		if err != nil {
			return err
		}
		revoked, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil || len(revoked) == 0 {
			return err
		}
		return audit(ctx, tx, p, "external_agent.authorization.revoke", p.Subject, revoked)
	})
}
