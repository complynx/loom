package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
)

// SaveOutputValidation atomically records a pure validation checkpoint, with no billing or public event.
func (s *Store) SaveOutputValidation(
	ctx context.Context,
	rid, owner string,
	fence int64,
	input, result json.RawMessage,
) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		r, err := leaseRun(ctx, tx, rid, owner, fence)
		if err != nil {
			return err
		}
		if r.State != domain.Running || r.PauseRequested || r.CancelRequested {
			return domain.Conflict("stop_requested")
		}
		var pending bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM steps WHERE run_id=$1 AND state IN('pending','unknown'))", rid).
			Scan(&pending); err != nil {
			return err
		}
		if pending {
			return domain.Conflict("step_outcome_unknown")
		}
		if !json.Valid(input) || !json.Valid(result) || len(result) > 65536 {
			return domain.Conflict("invalid_output_validation")
		}
		_, err = tx.Exec(
			ctx,
			`INSERT INTO steps(id,run_id,ordinal,kind,state,input,result,usage,reserve) SELECT $1,$2,COALESCE(max(ordinal),0)+1,'output_validation','completed',$3,$4,$5,0 FROM steps WHERE run_id=$2`,
			id(),
			rid,
			input,
			result,
			document(domain.Usage{}),
		)
		return err
	})
}
