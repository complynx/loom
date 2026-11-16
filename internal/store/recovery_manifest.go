package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
)

const recoveryReceiptVersion = 1

// The predecessor is locked and stopped before this snapshot. New successors
// inherit immutable receipt references, not historical consent or authority.
func captureRecoveryReceiptIDs(ctx context.Context, tx pgx.Tx, r domain.Run) ([]string, error) {
	ids, err := inheritedRecoveryReceiptIDs(r)
	if err != nil {
		return nil, err
	}
	own, err := recoveryIDsForRuns(ctx, tx, []string{r.ID})
	if err != nil {
		return nil, err
	}
	ids = append(ids, own...)
	if len(ids) > MaxRecoveryOperations {
		return nil, domain.Conflict("recovery_context_too_large")
	}
	return ids, nil
}

func inheritedRecoveryReceiptIDs(r domain.Run) ([]string, error) {
	var ids []string
	switch r.RecoveryReceiptVersion {
	case recoveryReceiptVersion:
		ids = append([]string{}, r.RecoveryOperationIDs...)
	case 0:
		if r.ReplacesRunID == "" {
			return ids, nil
		}
		return nil, domain.Conflict("recovery_context_unavailable")
	default:
		return nil, domain.Conflict("recovery_context_unavailable")
	}
	if len(ids) > MaxRecoveryOperations {
		return nil, domain.Conflict("recovery_context_too_large")
	}
	return ids, nil
}

func recoveryIDsForRuns(ctx context.Context, db pgx.Tx, sources []string) ([]string, error) {
	ids := []string{}
	if len(sources) == 0 {
		return ids, nil
	}
	rows, err := db.Query(
		ctx,
		"SELECT w.id::text FROM write_operations w JOIN runs r ON r.id=w.run_id WHERE w.run_id=ANY($1::uuid[]) ORDER BY (r.doc->>'admission_sequence')::bigint,w.created_at,w.id LIMIT $2",
		sources,
		MaxRecoveryOperations+1,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
		if len(ids) > MaxRecoveryOperations {
			return nil, domain.Conflict("recovery_context_too_large")
		}
	}
	return ids, rows.Err()
}
