package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
)

// ExecutionCachedContext is a hint. Authorization and current model policy remain separate.
func (s *Store) ExecutionCachedContext(ctx context.Context, rid string) (domain.CachedContext, error) {
	var out domain.CachedContext
	current, err := s.ExecutionRun(ctx, rid)
	if err != nil || current.PreviousRunID == "" {
		return out, err
	}
	previous, err := s.ExecutionRun(ctx, current.PreviousRunID)
	if err != nil {
		return out, err
	}
	if previous.State != domain.Completed || previous.ChatID != current.ChatID || previous.OwnerID != current.OwnerID ||
		previous.OrgID != current.OrgID ||
		previous.ActorID != current.ActorID ||
		previous.ModelID != current.ModelID ||
		previous.Effort != current.Effort ||
		hash(previous.Model) != hash(current.Model) ||
		previous.SettingsSnapshot.Version != current.SettingsSnapshot.Version ||
		len(previous.Input.ArtifactIDs) > 0 ||
		len(current.Input.ArtifactIDs) > 0 {
		return out, nil
	}
	var raw []byte
	err = s.pool.QueryRow(ctx, `SELECT c.through_seq,c.items FROM run_contexts c JOIN chats ch ON ch.id=c.chat_id WHERE c.run_id=$1 AND c.chat_id=$2 AND ch.deleted_at IS NULL AND c.expires_at>now() AND NOT EXISTS(SELECT 1 FROM chat_events excluded WHERE excluded.run_id=c.run_id AND excluded.seq<=c.through_seq AND excluded.kind IN('input.steering','input.answer','question.created','approval.created')) AND c.source_count=(SELECT count(*) FROM chat_events WHERE chat_id=c.chat_id AND seq<=c.through_seq)`, previous.ID, current.ChatID).
		Scan(&out.ThroughSeq, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CachedContext{}, nil
	}
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal(raw, &out.Items); err != nil {
		return domain.CachedContext{}, err
	}
	return out, nil
}

// SaveRunContext keeps only the history prefix, never model/tool execution steps.
func (s *Store) SaveRunContext(
	ctx context.Context,
	rid, owner string,
	fence, through int64,
	items []json.RawMessage,
) error {
	if through <= 0 {
		return nil
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		r, err := leaseRun(ctx, tx, rid, owner, fence)
		if err != nil {
			return err
		}
		if len(r.Input.ArtifactIDs) > 0 {
			return nil
		}
		raw, err := json.Marshal(items)
		if err != nil {
			return err
		}
		_, err = tx.Exec(
			ctx,
			`INSERT INTO run_contexts(run_id,chat_id,through_seq,source_count,items,expires_at) SELECT $1,$2,$3,count(*),$4,$5 FROM chat_events WHERE chat_id=$2 AND seq<=$3 ON CONFLICT(run_id) DO UPDATE SET through_seq=EXCLUDED.through_seq,source_count=EXCLUDED.source_count,items=EXCLUDED.items,expires_at=EXCLUDED.expires_at`,
			rid,
			r.ChatID,
			through,
			raw,
			time.Now().Add(contextCacheTTL),
		)
		return err
	})
}
