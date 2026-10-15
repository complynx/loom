package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
)

// CheckSessionKeys refuses startup if a stored envelope needs an absent key.
// This includes expired and revoked records, which can still exist in backups.
func (s *Store) CheckSessionKeys(ctx context.Context, hasKey func(string) bool) error {
	rows, err := s.pool.Query(ctx, "SELECT DISTINCT COALESCE(envelope->>'key_id','') FROM auth_sessions")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if scanErr := rows.Scan(&id); scanErr != nil {
			return scanErr
		}
		if id == "" || !hasKey(id) {
			return errors.New(
				"stored sessions require an unavailable encryption key; restore the keyring before startup",
			)
		}
	}
	return rows.Err()
}

// RotateSessionKeys locks a bounded batch so reauthorization and revocation cannot
// be overwritten. A failed or interrupted batch rolls back; earlier batches remain.
// Cryptographic-only updates preserve the client-visible authorization version.
func (s *Store) RotateSessionKeys(
	ctx context.Context,
	active string,
	limit int,
	rewrap func(domain.AuthSession) ([]byte, error),
) (int, error) {
	if active == "" || limit < 1 || limit > 1000 || rewrap == nil {
		return 0, errors.New("invalid session rotation batch")
	}
	count := 0
	err := s.tx(ctx, func(tx pgx.Tx) error {
		count = 0
		rows, err := tx.Query(
			ctx,
			"SELECT id::text,issuer,subject,actor,org_id,envelope,expires_at,version,revoked_at FROM auth_sessions WHERE envelope->>'key_id' IS DISTINCT FROM $1 ORDER BY id LIMIT $2 FOR UPDATE",
			active,
			limit,
		)
		if err != nil {
			return err
		}
		batch := make([]domain.AuthSession, 0, limit)
		for rows.Next() {
			var v domain.AuthSession
			if scanErr := rows.Scan(
				&v.ID,
				&v.Issuer,
				&v.Subject,
				&v.Actor,
				&v.OrgID,
				&v.Envelope,
				&v.ExpiresAt,
				&v.Version,
				&v.RevokedAt,
			); scanErr != nil {
				rows.Close()
				return scanErr
			}
			batch = append(batch, v)
		}
		rows.Close()
		if rowsErr := rows.Err(); rowsErr != nil {
			return rowsErr
		}
		for _, v := range batch {
			envelope, rewrapErr := rewrap(v)
			if rewrapErr != nil {
				return rewrapErr
			}
			if _, updateErr := tx.Exec(
				ctx,
				"UPDATE auth_sessions SET envelope=$2 WHERE id=$1",
				v.ID,
				envelope,
			); updateErr != nil {
				return updateErr
			}
			count++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}
