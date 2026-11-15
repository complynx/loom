package store

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/operations"
)

const (
	MaxRecoveryOperations   = 64
	MaxRecoveryReceiptBytes = 65536
)

// RecoveryOperations reads bounded predecessor receipts only within the same
// chat and identity. The caller must reauthorize each module before disclosure.
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
	var ancestors []domain.Run
	previousID := current.ReplacesRunID
	for previousID != "" {
		if len(ancestors) >= domain.MaxReceiptChainRuns {
			break
		}
		previous, e := s.ExecutionRun(ctx, previousID)
		if e != nil {
			return nil, e
		}
		if previous.ChatID != current.ChatID || previous.OwnerID != current.OwnerID ||
			previous.ActorID != current.ActorID ||
			previous.OrgID != current.OrgID ||
			!previous.State.Terminal() ||
			previous.ReservedMicrocredits != 0 {
			return nil, domain.Forbidden()
		}
		ancestors = append(ancestors, previous)
		previousID = previous.ReplacesRunID
	}
	out := []operations.Operation{}
	for _, ancestor := range slices.Backward(ancestors) {
		out, err = s.appendRecoveryOperations(ctx, ancestor, out)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) appendRecoveryOperations(
	ctx context.Context,
	run domain.Run,
	out []operations.Operation,
) ([]operations.Operation, error) {
	var hasOperations, hasHistory bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM write_operations WHERE run_id=$1),EXISTS(SELECT 1 FROM chat_events WHERE chat_id=$2 AND seq=$3 AND run_id=$1 AND kind='message.user')`, run.ID, run.ChatID, run.AdmissionSequence).
		Scan(&hasOperations, &hasHistory); err != nil {
		return nil, err
	}
	if hasOperations && !hasHistory {
		return nil, domain.Conflict("recovery_context_unavailable")
	}
	rows, err := s.pool.Query(
		ctx,
		"SELECT CASE WHEN octet_length(doc::text)<=$3 THEN doc ELSE NULL END,state FROM write_operations WHERE run_id=$1 ORDER BY created_at,id LIMIT $2",
		run.ID,
		MaxRecoveryOperations-len(out)+1,
		MaxRecoveryReceiptBytes,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var state string
		if err = rows.Scan(&raw, &state); err != nil {
			return nil, err
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
		op.State = state
		out = append(out, op)
	}
	return out, rows.Err()
}
