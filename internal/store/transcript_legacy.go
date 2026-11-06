package store

import (
	"context"
	"encoding/json"

	"github.com/complynx/loom/internal/domain"
)

// LegacyTranscript finds pre-upgrade paid work before any new processing is admitted.
func (s *Store) LegacyTranscript(
	ctx context.Context,
	p domain.Principal,
	sourceID, key string,
	parameters json.RawMessage,
) (Transcript, error) {
	var out Transcript
	if !p.Has("artifacts.read") || !p.Has("chat.read") {
		return out, domain.Forbidden()
	}
	err := s.pool.QueryRow(ctx, `SELECT s.id::text,original.id::text,s.state,s.result,s.input
 FROM steps s JOIN runs r ON r.id=s.run_id
 JOIN artifacts a ON a.id=NULLIF(s.input#>>'{prepared,source_artifact_id}','')::uuid
 JOIN artifacts original ON original.id=COALESCE(a.source_reference_id,a.id)
 JOIN artifacts wanted ON wanted.id=$4
 WHERE s.kind='transcription' AND r.owner_id=(SELECT id FROM users WHERE issuer=$1 AND subject=$2)
 AND r.org_id=$3 AND original.owner_id=r.owner_id AND original.org_id=r.org_id
 AND wanted.owner_id=r.owner_id AND wanted.org_id=r.org_id
 AND original.sha256=wanted.sha256 AND original.size=wanted.size
 AND COALESCE(s.input->>'transcript_key','')=''
 AND NOT COALESCE((s.state='completed' AND s.usage=$7::jsonb AND s.result->>'output'=$8),false)
 AND COALESCE(NULLIF(s.input#>'{call,arguments,start}','null'::jsonb),'0'::jsonb)=$5::jsonb->'start'
 AND COALESCE(NULLIF(s.input#>'{call,arguments,end}','null'::jsonb),'0'::jsonb)=$5::jsonb->'end'
 AND NOT EXISTS(SELECT 1 FROM audio_transcripts t WHERE t.owner_id=r.owner_id AND t.org_id=r.org_id AND t.processing_key=$6)
 ORDER BY s.created_at,s.id LIMIT 1`, p.Issuer, p.Subject, p.OrgID, sourceID, parameters, key, document(domain.Usage{}), `{"error":"transcription_rejected"}`).
		Scan(&out.ID, &out.SourceArtifactID, &out.State, &out.Result, &out.Intent)
	return out, mapError(err)
}

// ArchiveLegacyTranscript also retains unavailable/uncertain legacy identities, preventing silent regeneration.
func (s *Store) ArchiveLegacyTranscript(
	ctx context.Context,
	p domain.Principal,
	record Transcript,
	key string,
	result json.RawMessage,
) error {
	if len(result) > maxTranscriptRecordBytes {
		return domain.Conflict("invalid_transcript_result")
	}
	_, err := s.pool.Exec(
		ctx,
		`INSERT INTO audio_transcripts(id,owner_id,org_id,source_artifact_id,processing_key,dispatched_at,result)
 SELECT s.id,r.owner_id,r.org_id,a.id,$5,s.created_at,$6 FROM steps s JOIN runs r ON r.id=s.run_id JOIN artifacts a ON a.id=$4
 WHERE s.id=$7 AND r.owner_id=(SELECT id FROM users WHERE issuer=$1 AND subject=$2) AND r.org_id=$3 AND a.owner_id=r.owner_id AND a.org_id=r.org_id
 ON CONFLICT DO NOTHING`,
		p.Issuer,
		p.Subject,
		p.OrgID,
		record.SourceArtifactID,
		key,
		result,
		record.ID,
	)
	return err
}
