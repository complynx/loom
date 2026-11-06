package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
)

// FindContentTranscript also finds retained pre-content-key receipts without changing their provenance.
func (s *Store) FindContentTranscript(
	ctx context.Context,
	p domain.Principal,
	key, reprocess, sourceID string,
	requestedRange json.RawMessage,
) (Transcript, error) {
	return s.transcript(ctx, p, `(a.processing_key=$4 OR EXISTS (
 SELECT 1 FROM artifacts original JOIN artifacts wanted ON wanted.id=$6
 WHERE original.id=a.source_artifact_id AND original.owner_id=a.owner_id AND original.org_id=a.org_id
 AND wanted.owner_id=a.owner_id AND wanted.org_id=a.org_id
 AND original.sha256=wanted.sha256 AND original.size=wanted.size
 AND COALESCE(NULLIF(s.input#>'{call,arguments,start}','null'::jsonb),'0'::jsonb)=$7::jsonb->'start'
 AND COALESCE(NULLIF(s.input#>'{call,arguments,end}','null'::jsonb),'0'::jsonb)=$7::jsonb->'end'
 )) AND a.reprocess_key=$5
 ORDER BY CASE WHEN s.state IN ('pending','unknown') THEN 0 ELSE 1 END,a.created_at,a.id LIMIT 1`,
		key, reprocess, sourceID, requestedRange)
}

// Guard all retained and legacy paid work under the caller's content/range admission lock.
// The lookup and reservation can race; completed work must also prevent another paid admission.
func guardTranscriptContent(
	ctx context.Context,
	tx pgx.Tx,
	r domain.Run,
	step domain.Step,
	intent domain.PaidToolIntent,
) error {
	var requested struct {
		Start float64 `json:"start"`
		End   float64 `json:"end"`
	}
	if err := json.Unmarshal(intent.Call.Arguments, &requested); err != nil {
		return err
	}
	raw, err := json.Marshal(requested)
	if err != nil {
		return err
	}
	var exists bool
	err = tx.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM steps previous JOIN runs prior_run ON prior_run.id=previous.run_id
 JOIN artifacts candidate ON candidate.id=COALESCE(NULLIF(previous.input->>'transcript_source_id',''),
 NULLIF(previous.input#>>'{prepared,source_artifact_id}',''))::uuid
 JOIN artifacts original ON original.id=COALESCE(candidate.source_reference_id,candidate.id)
 JOIN artifacts wanted ON wanted.id=$4
 WHERE previous.kind='transcription' AND previous.id<>$6
 AND prior_run.owner_id=$1 AND prior_run.org_id=$2
 AND original.owner_id=$1 AND original.org_id=$2 AND wanted.owner_id=$1 AND wanted.org_id=$2
 AND original.sha256=wanted.sha256 AND original.size=wanted.size
 AND COALESCE(NULLIF(previous.input#>'{call,arguments,start}','null'::jsonb),'0'::jsonb)=$5::jsonb->'start'
 AND COALESCE(NULLIF(previous.input#>'{call,arguments,end}','null'::jsonb),'0'::jsonb)=$5::jsonb->'end'
 AND (previous.state IN ('pending','unknown') OR (
 previous.state='completed' AND COALESCE(previous.input->>'reprocess_key','')=$3
 AND NOT COALESCE((previous.usage=$7::jsonb AND previous.result->>'output'=$8),false)))
 )`, r.OwnerID, r.OrgID, intent.ReprocessKey, intent.TranscriptSourceID, raw, step.ID,
		document(domain.Usage{}), `{"error":"transcription_rejected"}`).Scan(&exists)
	if err != nil {
		return err
	}
	if exists {
		return domain.Conflict("transcription_in_progress")
	}
	return nil
}
