package store

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
)

// Transcript retains the complete provider result separately from expiring binary artifacts.
// State is the original paid step's state; a result is reusable only after settlement.
type Transcript struct {
	ID, SourceArtifactID, State string
	Result, Intent              json.RawMessage
}

// The provider response is bounded to 1 MiB; JSON escaping can expand its text sixfold.
const maxTranscriptRecordBytes = 8 << 20

// TranscriptionSource returns owned provenance, including collected tombstones, without fetching bytes.
func (s *Store) TranscriptionSource(ctx context.Context, p domain.Principal, rid, aid string) (domain.Artifact, error) {
	var a domain.Artifact
	if !p.Has("artifacts.read") || !p.Has("chat.read") {
		return a, domain.Forbidden()
	}
	if _, err := uuid.Parse(aid); err != nil {
		return a, domain.NotFound()
	}
	err := s.pool.QueryRow(ctx, `SELECT original.id::text,original.owner_id::text,original.org_id,original.mime,original.sha256,original.size,original.external_disclosure_allowed,original.source_service_id,original.source_external_id,original.source_module,original.source_method,original.source_fetch_method
 FROM artifacts candidate JOIN artifacts original ON original.id=COALESCE(candidate.source_reference_id,candidate.id)
 JOIN runs r ON r.id=$2 JOIN chats run_chat ON run_chat.id=r.chat_id
 WHERE candidate.id=$1 AND candidate.owner_id=r.owner_id AND original.owner_id=r.owner_id AND candidate.org_id=r.org_id AND original.org_id=r.org_id
 AND r.owner_id=(SELECT id FROM users WHERE issuer=$3 AND subject=$4) AND r.org_id=$5 AND r.actor_id=$6 AND run_chat.deleted_at IS NULL
 AND (EXISTS(SELECT 1 FROM chat_artifacts ca JOIN chats c ON c.id=ca.chat_id JOIN artifacts attached ON attached.id=ca.artifact_id WHERE COALESCE(attached.source_reference_id,attached.id)=original.id AND c.owner_id=r.owner_id AND c.org_id=r.org_id AND c.deleted_at IS NULL)
 OR (NOT EXISTS(SELECT 1 FROM chat_artifacts all_ca JOIN artifacts all_a ON all_a.id=all_ca.artifact_id WHERE COALESCE(all_a.source_reference_id,all_a.id)=original.id) AND (EXISTS(SELECT 1 FROM artifact_handles h JOIN runs hr ON hr.id=h.run_id JOIN chats hc ON hc.id=hr.chat_id JOIN artifacts held ON held.id=h.artifact_id WHERE COALESCE(held.source_reference_id,held.id)=original.id AND h.owner_id=r.owner_id AND h.org_id=r.org_id AND hc.deleted_at IS NULL) OR EXISTS(SELECT 1 FROM audio_transcripts retained JOIN steps retained_step ON retained_step.id=retained.id JOIN runs retained_run ON retained_run.id=retained_step.run_id JOIN chats retained_chat ON retained_chat.id=retained_run.chat_id WHERE retained.source_artifact_id=original.id AND retained.owner_id=r.owner_id AND retained.org_id=r.org_id AND retained_chat.deleted_at IS NULL))))`, aid, rid, p.Issuer, p.Subject, p.OrgID, actor(p)).
		Scan(
			&a.ID, &a.OwnerID, &a.OrgID, &a.MIME, &a.SHA256, &a.Size, &a.ExternalDisclosureAllowed, &a.SourceServiceID, &a.SourceExternalID, &a.SourceModule, &a.SourceMethod, &a.SourceFetchMethod)
	return a, mapError(err)
}

func bindTranscriptStep(ctx context.Context, tx pgx.Tx, r domain.Run, step domain.Step) error {
	if step.Kind != "transcription" {
		return nil
	}
	var intent domain.PaidToolIntent
	if err := json.Unmarshal(step.Input, &intent); err != nil {
		return err
	}
	if intent.TranscriptKey == "" {
		return nil
	} // Legacy frozen paid intents have no durable transcript identity.
	const processingKeyLength = 64
	if len(intent.TranscriptKey) != processingKeyLength {
		return domain.Conflict("invalid_transcript_identity")
	}
	_, err := tx.Exec(
		ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1,0))`,
		r.OwnerID+":"+r.OrgID+":"+intent.TranscriptKey,
	)
	if err != nil {
		return err
	}
	var busy bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM audio_transcripts a JOIN steps s ON s.id=a.id WHERE a.owner_id=$1 AND a.org_id=$2 AND a.processing_key=$3 AND s.state IN ('pending','unknown'))`, r.OwnerID, r.OrgID, intent.TranscriptKey).
		Scan(&busy)
	if err != nil {
		return err
	}
	if busy {
		return domain.Conflict("transcription_in_progress")
	}
	tag, err := tx.Exec(
		ctx,
		`INSERT INTO audio_transcripts(id,owner_id,org_id,source_artifact_id,processing_key,reprocess_key) SELECT $1,$2,$3,a.id,$5,$6 FROM artifacts a WHERE a.id=$4 AND a.owner_id=$2 AND a.org_id=$3 ON CONFLICT(owner_id,org_id,processing_key,reprocess_key) DO NOTHING`,
		step.ID,
		r.OwnerID,
		r.OrgID,
		intent.TranscriptSourceID,
		intent.TranscriptKey,
		intent.ReprocessKey,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return domain.Conflict("transcription_in_progress")
	}
	return nil
}

// FindTranscript is owner-scoped before it returns state, content or provenance.
func (s *Store) FindTranscript(ctx context.Context, p domain.Principal, key, reprocess string) (Transcript, error) {
	return s.transcript(ctx, p, `a.processing_key=$4 AND a.reprocess_key=$5`, key, reprocess)
}
func (s *Store) ReadTranscript(ctx context.Context, p domain.Principal, tid string) (Transcript, error) {
	if _, err := uuid.Parse(tid); err != nil {
		return Transcript{}, domain.NotFound()
	}
	return s.transcript(ctx, p, `a.id=$4`, tid)
}
func (s *Store) transcript(ctx context.Context, p domain.Principal, filter string, args ...any) (Transcript, error) {
	var out Transcript
	if !p.Has("artifacts.read") || !p.Has("chat.read") {
		return out, domain.Forbidden()
	}
	params := []any{p.Issuer, p.Subject, p.OrgID}
	params = append(params, args...)
	err := s.pool.QueryRow(ctx, `SELECT a.id::text,a.source_artifact_id::text,s.state,a.result,s.input FROM audio_transcripts a JOIN steps s ON s.id=a.id WHERE a.owner_id=(SELECT id FROM users WHERE issuer=$1 AND subject=$2) AND a.org_id=$3 AND `+filter, params...).
		Scan(&out.ID, &out.SourceArtifactID, &out.State, &out.Result, &out.Intent)
	return out, mapError(err)
}

// ClaimTranscriptDispatch is irreversible. A crash after this point requires reconciliation, never resending.
func (s *Store) ClaimTranscriptDispatch(ctx context.Context, r domain.Run, sid string) error {
	tag, err := s.pool.Exec(
		ctx,
		`UPDATE audio_transcripts a SET dispatched_at=now() FROM steps s,runs r WHERE a.id=$1 AND s.id=a.id AND s.run_id=r.id AND r.id=$2 AND r.owner_id=$3 AND r.org_id=$4 AND r.lease_owner=$5 AND r.fence=$6 AND r.state='running' AND r.lease_until>now() AND NOT r.cancel_requested AND NOT r.pause_requested AND s.state='pending' AND a.dispatched_at IS NULL`,
		sid,
		r.ID,
		r.OwnerID,
		r.OrgID,
		r.LeaseOwner,
		r.Fence,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return domain.Conflict("transcription_dispatch_uncertain")
	}
	return nil
}
func (s *Store) SaveTranscript(ctx context.Context, r domain.Run, sid string, result json.RawMessage) error {
	if !json.Valid(result) || len(result) > maxTranscriptRecordBytes {
		return domain.Conflict("invalid_transcript_result")
	}
	tag, err := s.pool.Exec(
		ctx,
		`UPDATE audio_transcripts a SET result=$4 FROM steps s WHERE a.id=$1 AND s.id=a.id AND s.run_id=$2 AND a.owner_id=$3 AND a.org_id=$5 AND a.dispatched_at IS NOT NULL AND a.result IS NULL`,
		sid,
		r.ID,
		r.OwnerID,
		result,
		r.OrgID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return domain.Conflict("transcript_result_conflict")
	}
	return nil
}
