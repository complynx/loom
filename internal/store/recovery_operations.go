package store

import (
	"context"
	"encoding/json"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/operations"
)

const (
	MaxRecoveryOperations   = 64
	MaxRecoveryReceiptBytes = 65536
)

// RecoveryOperations loads the successor's bounded receipt manifest.
// The caller must reauthorize every module before disclosure.
func (s *Store) RecoveryOperations(ctx context.Context, rid string) ([]operations.Operation, error) {
	current, err := s.ExecutionRun(ctx, rid)
	if err != nil {
		return nil, err
	}
	var live bool
	if err = s.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM chats WHERE id=$1 AND deleted_at IS NULL)", current.ChatID).
		Scan(&live); err != nil {
		return nil, err
	}
	if !live {
		return nil, domain.NotFound()
	}
	if current.ReplacesRunID == "" {
		return []operations.Operation{}, nil
	}
	ids, err := inheritedRecoveryReceiptIDs(current)
	if err != nil {
		return nil, err
	}
	out := []operations.Operation{}
	if len(ids) == 0 {
		return out, nil
	}
	return s.readRecoveryReceipts(ctx, current, ids)
}

func (s *Store) readRecoveryReceipts(
	ctx context.Context,
	current domain.Run,
	ids []string,
) ([]operations.Operation, error) {
	out := []operations.Operation{}
	rows, err := s.pool.Query(
		ctx,
		`
 SELECT w.id::text,w.run_id::text,
 CASE WHEN octet_length(w.doc::text)<=$3 THEN w.doc ELSE NULL END,w.state,
 (r.chat_id=$4::uuid AND r.owner_id=$5::uuid AND r.actor_id=$6 AND r.org_id=$7
  AND r.state IN('completed','cancelled','failed') AND r.reserved_microcredits=0
  AND COALESCE((r.doc->>'admission_sequence')::bigint,0)>0
  AND (r.doc->>'admission_sequence')::bigint<$8),
 EXISTS(SELECT 1 FROM chat_events e WHERE e.chat_id=$4::uuid AND e.seq=(r.doc->>'admission_sequence')::bigint AND e.run_id=r.id AND e.kind='message.user')
 FROM write_operations w JOIN runs r ON r.id=w.run_id
 WHERE w.id=ANY($1::uuid[])
 ORDER BY (r.doc->>'admission_sequence')::bigint,w.created_at,w.id LIMIT $2`,
		ids,
		MaxRecoveryOperations+1,
		MaxRecoveryReceiptBytes,
		current.ChatID,
		current.OwnerID,
		current.ActorID,
		current.OrgID,
		current.AdmissionSequence,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var id, runID, state string
		var valid, hasHistory bool
		if err = rows.Scan(&id, &runID, &raw, &state, &valid, &hasHistory); err != nil {
			return nil, err
		}
		if !valid {
			return nil, domain.Forbidden()
		}
		if !hasHistory {
			return nil, domain.Conflict("recovery_context_unavailable")
		}
		if len(raw) == 0 || len(out) >= MaxRecoveryOperations {
			return nil, domain.Conflict("recovery_context_too_large")
		}
		if state == "dispatching" || state == "pending" || state == "unknown" {
			return nil, domain.Conflict(uncertainEffectReason)
		}
		var op operations.Operation
		if err = json.Unmarshal(raw, &op); err != nil {
			return nil, err
		}
		op.ID, op.RunID, op.State = id, runID, state
		out = append(out, op)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(out) != len(ids) {
		return nil, domain.Conflict("recovery_context_unavailable")
	}
	return out, nil
}
