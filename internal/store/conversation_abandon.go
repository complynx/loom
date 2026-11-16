package store

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
)

// AbandonChatMessage explicitly ends delivery attempts, preserving the immutable
// original text and receipt. It never cancels work already delivered to a run.
func (s *Store) AbandonChatMessage(
	ctx context.Context,
	p domain.Principal,
	cid, mid, key string,
) (domain.RecoveryReceipt, error) {
	var out domain.RecoveryReceipt
	if !p.Has("chat.read") || !p.Has("runs.control") {
		return out, domain.Forbidden()
	}
	if key == "" {
		return out, domain.NewError(http.StatusUnprocessableEntity, "idempotency_key_required")
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		out = domain.RecoveryReceipt{}
		uid, e := s.ensureUser(ctx, tx, p)
		if e != nil {
			return e
		}
		var live string
		if e = tx.QueryRow(ctx, "SELECT id::text FROM chats WHERE id=$1 AND owner_id=$2 AND org_id=$3 AND deleted_at IS NULL FOR UPDATE", cid, uid, p.OrgID).
			Scan(&live); e != nil {
			return e
		}
		if e = tx.QueryRow(ctx, "SELECT id::text,seq,delivered,abandoned FROM conversation_messages WHERE id=$1 AND chat_id=$2 AND owner_id=$3 AND org_id=$4 AND actor_id=$5", mid, cid, uid, p.OrgID, actor(p)).
			Scan(&out.MessageID, &out.Sequence, &out.Delivered, &out.Abandoned); e != nil {
			return e
		}
		if out.Delivered {
			return domain.Conflict("message_already_delivered")
		}
		if e = persistAbandonment(ctx, tx, p, cid, mid, key, &out); e != nil {
			return e
		}
		out.Accepted, out.Abandoned = true, true
		abandonedReceipt(&out)
		return nil
	})
	return out, err
}

func abandonedReceipt(out *domain.RecoveryReceipt) {
	out.RunID, out.InputID = "", ""
	out.Status, out.Reason, out.NextAction = "abandoned", "explicit_withdrawal", "continue_or_create_new_request"
}

func persistAbandonment(
	ctx context.Context,
	tx pgx.Tx,
	p domain.Principal,
	cid, mid, key string,
	out *domain.RecoveryReceipt,
) error {
	sc := scope(p, "abandon-message:"+cid)
	old, e := idem(ctx, tx, sc, key, mid)
	if e != nil {
		return e
	}
	if old == "" {
		if e = saveIdem(ctx, tx, sc, key, mid, mid); e != nil {
			return e
		}
	}
	if !out.Abandoned {
		if _, e = tx.Exec(ctx, "UPDATE conversation_messages SET abandoned=true WHERE id=$1", mid); e != nil {
			return e
		}
		if e = audit(
			ctx,
			tx,
			p,
			"conversation_message.abandon",
			mid,
			map[string]any{"chat_id": cid, "sequence": out.Sequence},
		); e != nil {
			return e
		}
	}

	return nil
}
