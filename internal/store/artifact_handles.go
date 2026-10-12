package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
)

// CreateArtifactHandle binds a short-lived reference to the run owner and initiating actor.
func (s *Store) CreateArtifactHandle(
	ctx context.Context,
	p domain.Principal,
	rid, aid string,
) (domain.ArtifactHandle, error) {
	var out domain.ArtifactHandle
	if !p.Has("artifacts.read") || !p.Has("chat.read") {
		return out, domain.Forbidden()
	}
	if _, err := uuid.Parse(aid); err != nil {
		return out, domain.NotFound()
	}
	if _, err := uuid.Parse(rid); err != nil {
		return out, domain.NotFound()
	}
	uid, err := s.ResolveUser(ctx, p)
	if err != nil {
		return out, err
	}
	out = domain.ArtifactHandle{ID: id(), RunID: rid, ArtifactID: aid, ExpiresAt: time.Now().Add(handleTTL)}
	out.ContentPath = "/v1/artifact-handles/" + out.ID + "/content"
	err = s.tx(ctx, func(tx pgx.Tx) error {
		originalID := out.ID
		query := `INSERT INTO artifact_handles(id,run_id,artifact_id,owner_id,org_id,actor_id,expires_at) SELECT $1,r.id,a.id,r.owner_id,r.org_id,r.actor_id,LEAST($4,a.expires_at) FROM runs r JOIN chats c ON c.id=r.chat_id JOIN artifacts a ON a.id=$3 WHERE r.id=$2 AND r.owner_id=$5 AND r.org_id=$6 AND r.actor_id=$7 AND c.deleted_at IS NULL AND a.owner_id=r.owner_id AND a.org_id=r.org_id AND a.deleted_at IS NULL AND a.expires_at>now() ON CONFLICT(run_id,artifact_id) DO UPDATE SET expires_at=EXCLUDED.expires_at RETURNING id::text,expires_at`
		if scanErr := tx.QueryRow(ctx, query, out.ID, rid, aid, out.ExpiresAt, uid, p.OrgID, actor(p)).
			Scan(&out.ID, &out.ExpiresAt); scanErr != nil {
			return mapError(scanErr)
		}
		out.ContentPath = "/v1/artifact-handles/" + out.ID + "/content"
		if out.ID == originalID {
			return runEvent(ctx, tx, rid, "artifact.ready", out)
		}
		return nil
	})
	if err != nil {
		return domain.ArtifactHandle{}, err
	}

	return out, nil
}

func (s *Store) LoadArtifactHandle(ctx context.Context, p domain.Principal, hid string) (domain.ArtifactHandle, error) {
	var out domain.ArtifactHandle
	if !p.Has("artifacts.read") || !p.Has("chat.read") {
		return out, domain.Forbidden()
	}
	if _, err := uuid.Parse(hid); err != nil {
		return out, domain.NotFound()
	}
	uid, err := s.ResolveUser(ctx, p)
	if err != nil {
		return out, err
	}
	err = s.pool.QueryRow(ctx, `SELECT h.id::text,h.run_id::text,replace(h.artifact_id::text,'-',''),h.expires_at FROM artifact_handles h JOIN runs r ON r.id=h.run_id JOIN chats c ON c.id=r.chat_id JOIN artifacts a ON a.id=h.artifact_id WHERE h.id=$1 AND h.owner_id=$2 AND h.org_id=$3 AND h.actor_id=$4 AND h.expires_at>now() AND c.deleted_at IS NULL AND a.deleted_at IS NULL AND a.expires_at>now()`, hid, uid, p.OrgID, actor(p)).
		Scan(&out.ID, &out.RunID, &out.ArtifactID, &out.ExpiresAt)
	if err != nil {
		return out, mapError(err)
	}
	out.ContentPath = "/v1/artifact-handles/" + out.ID + "/content"
	return out, nil
}
