package store

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/complynx/loom/internal/domain"
)

// FileRefreshLease bounds recovery after a refresher dies.
const FileRefreshLease = 2 * time.Second

// FileReference retains source identity after temporary bytes are collected.
type FileReference struct {
	Original  domain.Artifact
	CurrentID string
}

func (s *Store) FileReference(ctx context.Context, p domain.Principal, aid string) (FileReference, error) {
	var out FileReference
	if !p.Has("artifacts.read") || !p.Has("chat.read") {
		return out, domain.Forbidden()
	}
	if _, err := uuid.Parse(aid); err != nil {
		return out, domain.NotFound()
	}
	err := s.pool.QueryRow(ctx, `SELECT replace(original.id::text,'-',''),original.owner_id::text,original.org_id,original.mime,original.sha256,original.size,original.external_disclosure_allowed,original.source_service_id,original.source_external_id,original.source_module,original.source_method,original.source_fetch_method,replace(ref.current_artifact_id::text,'-','')
 FROM artifacts candidate JOIN artifact_file_refs ref ON ref.id=candidate.source_reference_id JOIN artifacts original ON original.id=ref.id
 WHERE candidate.id=$1 AND candidate.owner_id=(SELECT id FROM users WHERE issuer=$2 AND subject=$3) AND candidate.org_id=$4
 AND EXISTS(SELECT 1 FROM chat_artifacts ca JOIN chats c ON c.id=ca.chat_id JOIN artifacts attached ON attached.id=ca.artifact_id WHERE attached.source_reference_id=original.id AND c.deleted_at IS NULL)`, aid, p.Issuer, p.Subject, p.OrgID).
		Scan(&out.Original.ID, &out.Original.OwnerID, &out.Original.OrgID, &out.Original.MIME, &out.Original.SHA256, &out.Original.Size, &out.Original.ExternalDisclosureAllowed, &out.Original.SourceServiceID, &out.Original.SourceExternalID, &out.Original.SourceModule, &out.Original.SourceMethod, &out.Original.SourceFetchMethod, &out.CurrentID)
	out.Original.SourceReferenceID = out.Original.ID
	return out, mapError(err)
}

// ClaimFileRefresh takes a bounded durable lease; no connection is held during IO.
func (s *Store) ClaimFileRefresh(ctx context.Context, ref FileReference) (string, bool, error) {
	lease := uuid.NewString()
	tag, err := s.pool.Exec(
		ctx,
		`UPDATE artifact_file_refs SET lease_id=$3,lease_until=now()+make_interval(secs=>$4) WHERE id=$1 AND current_artifact_id=$2 AND (lease_until IS NULL OR lease_until<=now())`,
		ref.Original.ID,
		ref.CurrentID,
		lease,
		FileRefreshLease.Seconds(),
	)
	return lease, tag.RowsAffected() == 1, err
}
func (s *Store) ReleaseFileRefresh(ctx context.Context, referenceID, lease string) error {
	_, err := s.pool.Exec(
		ctx,
		"UPDATE artifact_file_refs SET lease_id=NULL,lease_until=NULL WHERE id=$1 AND lease_id=$2",
		referenceID,
		lease,
	)
	return err
}
func (s *Store) CompleteFileRefresh(ctx context.Context, ref FileReference, lease, newID string) error {
	tag, err := s.pool.Exec(
		ctx,
		`UPDATE artifact_file_refs ref SET current_artifact_id=a.id,lease_id=NULL,lease_until=NULL FROM artifacts a,artifacts original WHERE ref.id=$1 AND ref.lease_id=$2 AND ref.lease_until>now() AND a.id=$3 AND a.source_reference_id=ref.id AND original.id=ref.id AND a.owner_id=original.owner_id AND a.org_id=original.org_id AND a.sha256=original.sha256 AND a.mime=original.mime AND a.size=original.size AND a.deleted_at IS NULL AND a.expires_at>now()`,
		ref.Original.ID,
		lease,
		newID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return domain.Conflict("file_refresh_conflict")
	}
	return nil
}

// FileHandleReference reopens an expired handle only as an owned run file reference.
// Public streaming still uses LoadArtifactHandle and its strict deadline.
func (s *Store) FileHandleReference(ctx context.Context, p domain.Principal, rid, hid string) (string, error) {
	if !p.Has("artifacts.read") || !p.Has("chat.read") {
		return "", domain.Forbidden()
	}
	if _, err := uuid.Parse(hid); err != nil {
		return "", domain.NotFound()
	}
	var aid string
	err := s.pool.QueryRow(ctx, `SELECT replace(h.artifact_id::text,'-','') FROM artifact_handles h JOIN runs r ON r.id=h.run_id JOIN chats c ON c.id=r.chat_id WHERE h.id=$1 AND h.run_id=$2 AND h.owner_id=(SELECT id FROM users WHERE issuer=$3 AND subject=$4) AND h.org_id=$5 AND h.actor_id=$6 AND c.deleted_at IS NULL`, hid, rid, p.Issuer, p.Subject, p.OrgID, actor(p)).
		Scan(&aid)
	return aid, mapError(err)
}

func (s *Store) RenewFileRefresh(ctx context.Context, referenceID, lease string) error {
	tag, err := s.pool.Exec(
		ctx,
		"UPDATE artifact_file_refs SET lease_until=now()+make_interval(secs=>$3) WHERE id=$1 AND lease_id=$2 AND lease_until>now()",
		referenceID,
		lease,
		FileRefreshLease.Seconds(),
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return domain.Conflict("file_refresh_lease_lost")
	}
	return nil
}
