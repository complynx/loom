package store

import (
	"context"

	"github.com/complynx/clx-scripter/internal/domain"
	"github.com/jackc/pgx/v5"
)

// LifecycleResult reports committed changes without exposing retained content.
type LifecycleResult struct{ ExpiredRuns, HistoryEvents, Summaries, Artifacts int64 }

// DeleteChat tombstones a chat and fences its runs. Unknown operations and credit
// reservations remain available to the privileged reconciliation path.
func (s *Store) DeleteChat(ctx context.Context, p domain.Principal, cid string) error {
	if !p.Has("chat.delete") {
		return domain.Forbidden()
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		var deleted bool
		e := tx.QueryRow(ctx, "SELECT deleted_at IS NOT NULL FROM chats WHERE id=$1 AND owner_id=(SELECT id FROM users WHERE issuer=$2 AND subject=$3) AND org_id=$4 FOR UPDATE", cid, p.Issuer, p.Subject, p.OrgID).Scan(&deleted)
		if e != nil {
			return e
		}
		if deleted {
			return nil
		}
		if _, e = tx.Exec(ctx, "UPDATE chats SET deleted_at=clock_timestamp(),is_default=false WHERE id=$1", cid); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, "SELECT id::text FROM runs WHERE chat_id=$1 AND state NOT IN('completed','cancelled','failed') ORDER BY id FOR UPDATE", cid)
		if e != nil {
			return e
		}
		ids := []string{}
		for rows.Next() {
			var rid string
			if e = rows.Scan(&rid); e != nil {
				rows.Close()
				return e
			}
			ids = append(ids, rid)
		}
		rows.Close()
		if e = rows.Err(); e != nil {
			return e
		}
		for _, rid := range ids {
			if e = terminateLifecycleRun(ctx, tx, rid, "cancelled", "chat_deleted"); e != nil {
				return e
			}
		}
		if _, e = tx.Exec(ctx, "DELETE FROM context_summaries WHERE chat_id=$1", cid); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "DELETE FROM chat_events WHERE chat_id=$1", cid); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE artifacts a SET deleted_at=clock_timestamp() WHERE a.deleted_at IS NULL AND EXISTS(SELECT 1 FROM chat_artifacts ca WHERE ca.artifact_id=a.id AND ca.chat_id=$1) AND NOT EXISTS(SELECT 1 FROM chat_artifacts ca JOIN chats c ON c.id=ca.chat_id WHERE ca.artifact_id=a.id AND c.deleted_at IS NULL)`, cid); e != nil {
			return e
		}
		return audit(ctx, tx, p, "chat.delete", cid, map[string]any{"tombstoned": true, "recovery_records_retained": true})
	})
}

func terminateLifecycleRun(ctx context.Context, tx pgx.Tx, rid, state, reason string) error {
	if _, e := tx.Exec(ctx, "UPDATE runs SET state=$2,reason=$3,cancel_requested=true,fence=fence+1,lease_owner='',lease_until=NULL,updated_at=clock_timestamp() WHERE id=$1", rid, state, reason); e != nil {
		return e
	}
	if _, e := tx.Exec(ctx, "UPDATE steps SET state='unknown' WHERE run_id=$1 AND state='pending'", rid); e != nil {
		return e
	}
	return runEvent(ctx, tx, rid, "run.status", map[string]string{"status": state, "reason": reason})
}

// AttachChatArtifact records the ownership-checked chat reference used by deletion
// and retention. It never transfers an artifact to another user or organization.
func (s *Store) AttachChatArtifact(ctx context.Context, p domain.Principal, cid, aid string) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		var uid string
		e := tx.QueryRow(ctx, "SELECT owner_id::text FROM chats WHERE id=$1 AND owner_id=(SELECT id FROM users WHERE issuer=$2 AND subject=$3) AND org_id=$4 AND deleted_at IS NULL FOR SHARE", cid, p.Issuer, p.Subject, p.OrgID).Scan(&uid)
		if e != nil {
			return e
		}
		var valid bool
		e = tx.QueryRow(ctx, "SELECT true FROM artifacts WHERE id=$1 AND owner_id=$2 AND org_id=$3 AND deleted_at IS NULL FOR SHARE", aid, uid, p.OrgID).Scan(&valid)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, "INSERT INTO chat_artifacts(chat_id,artifact_id) VALUES($1,$2) ON CONFLICT DO NOTHING", cid, aid)
		return e
	})
}

// SweepLifecycle expires durable deadlines and removes configured display data.
// It does not dispatch inference, release uncertain funds, or delete recovery,
// financial, credential, or audit records. A zero/unspecified retention is forever.
func (s *Store) SweepLifecycle(ctx context.Context, batch int) (LifecycleResult, error) {
	if batch <= 0 || batch > 1000 {
		batch = 100
	}
	var out LifecycleResult
	e := s.tx(ctx, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT id::text,CASE WHEN created_at+make_interval(secs=>COALESCE(NULLIF((doc#>>'{settings_snapshot,run_lifetime_seconds}')::int,0),604800))<=clock_timestamp() THEN 'run_expired' WHEN (COALESCE((doc#>>'{settings_snapshot,run_active_time_seconds}')::int,0)>0 AND active_elapsed_ms+CASE WHEN state='running' AND active_started_at IS NOT NULL THEN GREATEST(0,EXTRACT(EPOCH FROM (LEAST(clock_timestamp(),COALESCE(lease_until,clock_timestamp()))-active_started_at))*1000)::bigint ELSE 0 END >= COALESCE((doc#>>'{settings_snapshot,run_active_time_seconds}')::bigint,0)*1000) THEN 'active_time_expired' ELSE 'wait_expired' END FROM runs WHERE state NOT IN('completed','cancelled','failed') AND (
   created_at+make_interval(secs=>COALESCE(NULLIF((doc#>>'{settings_snapshot,run_lifetime_seconds}')::int,0),604800))<=clock_timestamp()
   OR (COALESCE((doc#>>'{settings_snapshot,run_active_time_seconds}')::int,0)>0 AND active_elapsed_ms+CASE WHEN state='running' AND active_started_at IS NOT NULL THEN GREATEST(0,EXTRACT(EPOCH FROM (LEAST(clock_timestamp(),COALESCE(lease_until,clock_timestamp()))-active_started_at))*1000)::bigint ELSE 0 END >= COALESCE((doc#>>'{settings_snapshot,run_active_time_seconds}')::bigint,0)*1000)
   OR state='waiting_input' AND (state_entered_at+make_interval(secs=>COALESCE(NULLIF((doc#>>'{settings_snapshot,input_wait_seconds}')::int,0),86400))<=clock_timestamp() OR (doc#>>'{question,expires_at}')::timestamptz<=clock_timestamp())
   OR state='waiting_approval' AND (state_entered_at+make_interval(secs=>COALESCE(NULLIF((doc#>>'{settings_snapshot,approval_wait_seconds}')::int,0),900))<=clock_timestamp() OR (doc#>>'{approval,expires_at}')::timestamptz<=clock_timestamp())
   OR state='paused' AND state_entered_at+make_interval(secs=>COALESCE(NULLIF((doc#>>'{settings_snapshot,paused_wait_seconds}')::int,0),604800))<=clock_timestamp())
   ORDER BY created_at LIMIT $1 FOR UPDATE SKIP LOCKED`, batch)
		if e != nil {
			return e
		}
		type expiry struct{ id, reason string }
		expired := []expiry{}
		for rows.Next() {
			var v expiry
			if e = rows.Scan(&v.id, &v.reason); e != nil {
				rows.Close()
				return e
			}
			expired = append(expired, v)
		}
		rows.Close()
		if e = rows.Err(); e != nil {
			return e
		}
		for _, v := range expired {
			if e = terminateLifecycleRun(ctx, tx, v.id, "failed", v.reason); e != nil {
				return e
			}
			out.ExpiredRuns++
		}
		tag, e := tx.Exec(ctx, `DELETE FROM chat_events WHERE (chat_id,seq) IN(SELECT ce.chat_id,ce.seq FROM chat_events ce JOIN chats c ON c.id=ce.chat_id WHERE c.retention_days>0 AND ce.received_at<clock_timestamp()-make_interval(days=>c.retention_days) AND NOT EXISTS(SELECT 1 FROM runs r WHERE r.chat_id=ce.chat_id AND r.state NOT IN('completed','cancelled','failed')) ORDER BY ce.received_at LIMIT $1)`, batch)
		if e != nil {
			return e
		}
		out.HistoryEvents = tag.RowsAffected()
		tag, e = tx.Exec(ctx, `DELETE FROM context_summaries WHERE chat_id IN(SELECT c.id FROM chats c JOIN context_summaries cs ON cs.chat_id=c.id WHERE c.retention_days>0 AND (cs.created_at<clock_timestamp()-make_interval(days=>c.retention_days) OR NOT EXISTS(SELECT 1 FROM chat_events original WHERE original.chat_id=c.id AND original.seq=1)) AND NOT EXISTS(SELECT 1 FROM runs r WHERE r.chat_id=c.id AND r.state NOT IN('completed','cancelled','failed')) LIMIT $1)`, batch)
		if e != nil {
			return e
		}
		out.Summaries = tag.RowsAffected()
		// Lock each candidate before rechecking live references. Admission takes a
		// share lock on the artifact, so expiry and a new run cannot race ownership.
		rows, e = tx.Query(ctx, `SELECT a.id::text FROM artifacts a WHERE a.deleted_at IS NULL AND a.retention_days>0 AND a.created_at<clock_timestamp()-make_interval(days=>a.retention_days) AND NOT EXISTS(SELECT 1 FROM chat_artifacts ca JOIN runs r ON r.chat_id=ca.chat_id WHERE ca.artifact_id=a.id AND r.state NOT IN('completed','cancelled','failed')) ORDER BY a.created_at LIMIT $1 FOR UPDATE OF a SKIP LOCKED`, batch)
		if e != nil {
			return e
		}
		aids := []string{}
		for rows.Next() {
			var aid string
			if e = rows.Scan(&aid); e != nil {
				rows.Close()
				return e
			}
			aids = append(aids, aid)
		}
		rows.Close()
		if e = rows.Err(); e != nil {
			return e
		}
		for _, aid := range aids {
			tag, e = tx.Exec(ctx, `UPDATE artifacts a SET deleted_at=clock_timestamp() WHERE a.id=$1 AND NOT EXISTS(SELECT 1 FROM chat_artifacts ca JOIN runs r ON r.chat_id=ca.chat_id WHERE ca.artifact_id=a.id AND r.state NOT IN('completed','cancelled','failed'))`, aid)
			if e != nil {
				return e
			}
			out.Artifacts += tag.RowsAffected()
		}
		return nil
	})
	return out, e
}
