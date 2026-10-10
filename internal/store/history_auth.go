package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/complynx/clx-scripter/internal/domain"
	"github.com/jackc/pgx/v5"
	"regexp"
	"time"
	"unicode/utf8"
)

func (s *Store) History(ctx context.Context, p domain.Principal, cid string, after int64, limit int) ([]domain.ChatEvent, error) {
	if !p.Has("chat.read") {
		return nil, domain.Forbidden()
	}
	uid, e := s.ResolveUser(ctx, p)
	if e != nil {
		return nil, e
	}
	if cid == "default" {
		c, e := s.DefaultChat(ctx, p)
		if e != nil {
			return nil, e
		}
		cid = c.ID
	}
	var owner string
	if e = s.pool.QueryRow(ctx, "SELECT owner_id::text FROM chats WHERE id=$1 AND owner_id=$2 AND org_id=$3 AND deleted_at IS NULL", cid, uid, p.OrgID).Scan(&owner); e != nil {
		return nil, mapError(e)
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	return s.history(ctx, cid, after, 0, limit, false)
}

const eventColumns = "seq,kind,action,text,doc,COALESCE(run_id::text,''),source_actor,COALESCE(source_event_id,''),occurred_at,received_at"

func (s *Store) history(ctx context.Context, cid string, after, before int64, limit int, descending bool) ([]domain.ChatEvent, error) {
	order := "ASC"
	if descending {
		order = "DESC"
	}
	rows, e := s.pool.Query(ctx, "SELECT "+eventColumns+" FROM chat_events WHERE chat_id=$1 AND chat_id IN(SELECT id FROM chats WHERE deleted_at IS NULL) AND seq>$2 AND ($3=0 OR seq<$3) ORDER BY seq "+order+" LIMIT $4", cid, after, before, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []domain.ChatEvent{}
	for rows.Next() {
		var v domain.ChatEvent
		if e = rows.Scan(&v.Seq, &v.Type, &v.Action, &v.Text, &v.Data, &v.RunID, &v.SourceActor, &v.SourceEventID, &v.OccurredAt, &v.ReceivedAt); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	if descending {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out, rows.Err()
}
func (s *Store) ExecutionHistory(ctx context.Context, rid string, limit int) ([]domain.ChatEvent, error) {
	r, e := s.ExecutionRun(ctx, rid)
	if e != nil {
		return nil, e
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	return s.history(ctx, r.ChatID, 0, 0, limit, true)
}
func (s *Store) AppendEvent(ctx context.Context, p domain.Principal, cid string, in domain.AppendEvent) (domain.ChatEvent, error) {
	originalHash := hash(in)
	var out domain.ChatEvent
	if !p.Has("chat.events.append") {
		return out, domain.Forbidden()
	}
	if in.SourceEventID == "" || len(in.SourceEventID) > 128 || len(in.Type) > 128 || len(in.Action) > 256 || in.Text == "" || len(in.Text) > 65536 || len(in.Data) > 65536 {
		return out, domain.NewError(422, "invalid_event")
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		uid, e := s.ensureUser(ctx, tx, p)
		if e != nil {
			return e
		}
		if cid == "default" {
			c, e := s.defaultChat(ctx, tx, uid, p.OrgID)
			if e != nil {
				return e
			}
			cid = c.ID
		}
		if e = checkChat(ctx, tx, uid, p.OrgID, cid); e != nil {
			return e
		}
		var previousHash, previousChat string
		var seq int64
		e = tx.QueryRow(ctx, "SELECT chat_id::text,seq,request_hash FROM chat_events WHERE source_actor=$1 AND source_event_id=$2", actor(p), in.SourceEventID).Scan(&previousChat, &seq, &previousHash)
		if e == nil {
			if previousChat != cid || previousHash != originalHash {
				return domain.Conflict("event_conflict")
			}
			return tx.QueryRow(ctx, "SELECT "+eventColumns+" FROM chat_events WHERE chat_id=$1 AND seq=$2", cid, seq).Scan(&out.Seq, &out.Type, &out.Action, &out.Text, &out.Data, &out.RunID, &out.SourceActor, &out.SourceEventID, &out.OccurredAt, &out.ReceivedAt)
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		out, e = chatEvent(ctx, tx, cid, "", in.Type, in.Text, actor(p), in.Data)
		if e != nil {
			return e
		}
		if in.OccurredAt.IsZero() {
			in.OccurredAt = out.OccurredAt
		}
		_, e = tx.Exec(ctx, "UPDATE chat_events SET source_event_id=$3,request_hash=$4,occurred_at=$5,action=$6 WHERE chat_id=$1 AND seq=$2", cid, out.Seq, in.SourceEventID, originalHash, in.OccurredAt, in.Action)
		out.Action = in.Action
		out.SourceEventID = in.SourceEventID
		out.OccurredAt = in.OccurredAt
		return e
	})
	return out, err
}
func (s *Store) Search(ctx context.Context, p domain.Principal, cid string, in domain.SearchRequest) (domain.SearchResult, error) {
	out := domain.SearchResult{Events: []domain.ChatEvent{}}
	if !p.Has("chat.read") {
		return out, domain.Forbidden()
	}
	settings, e := s.GetSettings(ctx)
	if e != nil {
		return out, e
	}
	settings = normalizeSettings(settings)
	if utf8.RuneCountInString(in.Query) > settings.RegexPatternMaxChars || in.Query == "" {
		return out, domain.NewError(422, "invalid_pattern")
	}
	if in.Limit <= 0 || in.Limit > 50 {
		in.Limit = 50
	}
	var re *regexp.Regexp
	switch in.Mode {
	case "regex":
		re, e = regexp.Compile(in.Query)
		if e != nil {
			return out, domain.NewError(422, "invalid_pattern")
		}
	case "fulltext":
	default:
		return out, domain.NewError(422, "invalid_search_mode")
	}
	uid, e := s.ResolveUser(ctx, p)
	if e != nil {
		return out, e
	}
	if cid == "default" {
		c, e := s.DefaultChat(ctx, p)
		if e != nil {
			return out, e
		}
		cid = c.ID
	}
	var own string
	if e = s.pool.QueryRow(ctx, "SELECT owner_id::text FROM chats WHERE id=$1 AND owner_id=$2 AND org_id=$3 AND deleted_at IS NULL", cid, uid, p.OrgID).Scan(&own); e != nil {
		return out, mapError(e)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(settings.RegexDeadlineMS)*time.Millisecond)
	defer cancel()
	if in.Mode == "fulltext" {
		rows, e := s.pool.Query(ctx, "SELECT "+eventColumns+" FROM chat_events WHERE chat_id=$1 AND chat_id IN(SELECT id FROM chats WHERE deleted_at IS NULL) AND seq>$2 AND ($3=0 OR seq<$3) AND ($4='' OR source_actor=$4) AND (COALESCE(cardinality($5::text[]),0)=0 OR kind=ANY($5)) AND to_tsvector('simple',text)@@plainto_tsquery('simple',$6) ORDER BY seq LIMIT $7", cid, in.AfterSeq, in.BeforeSeq, in.SourceServiceID, in.EventTypes, in.Query, in.Limit+1)
		if e != nil {
			return out, e
		}
		defer rows.Close()
		for rows.Next() {
			var v domain.ChatEvent
			if e = rows.Scan(&v.Seq, &v.Type, &v.Action, &v.Text, &v.Data, &v.RunID, &v.SourceActor, &v.SourceEventID, &v.OccurredAt, &v.ReceivedAt); e != nil {
				return out, e
			}
			if len(out.Events) == in.Limit {
				out.Partial = true
				break
			}
			out.Events = append(out.Events, v)
			out.NextCursor = v.Seq
		}
		return out, rows.Err()
	}
	scanned := 0
	cursor := in.AfterSeq
	for scanned < settings.RegexScanMaxBytes {
		batch, e := s.history(ctx, cid, cursor, in.BeforeSeq, 50, false)
		if e != nil {
			if errors.Is(e, context.DeadlineExceeded) && len(out.Events) > 0 {
				out.Partial = true
				return out, nil
			}
			return out, e
		}
		if len(batch) == 0 {
			return out, nil
		}
		for _, v := range batch {
			if len(v.Text) > settings.RegexScanMaxBytes {
				return out, domain.NewError(422, "regex_event_too_large")
			}
			if scanned+len(v.Text) > settings.RegexScanMaxBytes {
				out.Partial = true
				out.NextCursor = cursor
				return out, nil
			}
			scanned += len(v.Text)
			cursor = v.Seq
			out.NextCursor = cursor
			if in.SourceServiceID != "" && v.SourceActor != in.SourceServiceID {
				continue
			}
			if len(in.EventTypes) > 0 {
				matches := false
				for _, typ := range in.EventTypes {
					if typ == v.Type {
						matches = true
					}
				}
				if !matches {
					continue
				}
			}
			if re.MatchString(v.Text) {
				out.Events = append(out.Events, v)
				if len(out.Events) >= in.Limit {
					out.Partial = true
					return out, nil
				}
			}
		}
		if len(batch) < 50 {
			return out, nil
		}
	}
	out.Partial = true
	return out, nil
}
func (s *Store) CreateAuthSession(ctx context.Context, p domain.Principal, envelope json.RawMessage, expires time.Time) (domain.AuthSession, error) {
	v := domain.AuthSession{ID: id(), Issuer: p.Issuer, Subject: p.Subject, Actor: actor(p), OrgID: p.OrgID, Envelope: envelope, ExpiresAt: expires, Version: 1}
	if !json.Valid(envelope) || !expires.After(time.Now()) {
		return v, domain.NewError(422, "invalid_auth_session")
	}
	_, e := s.pool.Exec(ctx, "INSERT INTO auth_sessions(id,issuer,subject,actor,org_id,envelope,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)", v.ID, v.Issuer, v.Subject, v.Actor, v.OrgID, envelope, expires)
	return v, e
}
func (s *Store) LoadAuthSession(ctx context.Context, p domain.Principal, sid string) (domain.AuthSession, error) {
	var v domain.AuthSession
	e := s.pool.QueryRow(ctx, "SELECT id::text,issuer,subject,actor,org_id,envelope,expires_at,version,revoked_at FROM auth_sessions WHERE id=$1 AND issuer=$2 AND subject=$3 AND actor=$4 AND org_id=$5", sid, p.Issuer, p.Subject, actor(p), p.OrgID).Scan(&v.ID, &v.Issuer, &v.Subject, &v.Actor, &v.OrgID, &v.Envelope, &v.ExpiresAt, &v.Version, &v.RevokedAt)
	return v, mapError(e)
}
func (s *Store) ExecutionAuthSession(ctx context.Context, rid string) (domain.AuthSession, error) {
	r, e := s.ExecutionRun(ctx, rid)
	if e != nil {
		return domain.AuthSession{}, e
	}
	if r.AuthSessionID == "" {
		return domain.AuthSession{}, domain.NewError(401, "authorization_required")
	}
	var v domain.AuthSession
	e = s.pool.QueryRow(ctx, "SELECT id::text,issuer,subject,actor,org_id,envelope,expires_at,version,revoked_at FROM auth_sessions WHERE id=$1 AND revoked_at IS NULL AND expires_at>now()", r.AuthSessionID).Scan(&v.ID, &v.Issuer, &v.Subject, &v.Actor, &v.OrgID, &v.Envelope, &v.ExpiresAt, &v.Version, &v.RevokedAt)
	return v, mapError(e)
}
func (s *Store) ReauthorizeAuthSession(ctx context.Context, p domain.Principal, sid string, envelope json.RawMessage, expires time.Time, version int64) (domain.AuthSession, error) {
	var v domain.AuthSession
	if !json.Valid(envelope) || !expires.After(time.Now()) {
		return v, domain.NewError(422, "invalid_auth_session")
	}
	e := s.tx(ctx, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, "UPDATE auth_sessions SET envelope=$6,expires_at=$7,version=version+1,revoked_at=NULL WHERE id=$1 AND issuer=$2 AND subject=$3 AND actor=$4 AND org_id=$5 AND version=$8", sid, p.Issuer, p.Subject, actor(p), p.OrgID, envelope, expires, version)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return domain.Conflict("session_version_conflict")
		}
		rows, e := tx.Query(ctx, "UPDATE runs SET state='queued',reason='' WHERE state='waiting_auth' AND doc->>'auth_session_id'=$1 RETURNING id::text", sid)
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
			if e = runEvent(ctx, tx, rid, "run.status", map[string]string{"status": "queued"}); e != nil {
				return e
			}
			if s.enqueue != nil {
				if e = s.enqueue(ctx, tx, rid); e != nil {
					return e
				}
			}
		}
		return nil
	})
	if e != nil {
		return v, e
	}
	return s.LoadAuthSession(ctx, p, sid)
}
func (s *Store) RevokeAuthSession(ctx context.Context, p domain.Principal, sid string) error {
	tag, e := s.pool.Exec(ctx, "UPDATE auth_sessions SET revoked_at=now(),version=version+1 WHERE id=$1 AND issuer=$2 AND subject=$3 AND actor=$4 AND org_id=$5", sid, p.Issuer, p.Subject, actor(p), p.OrgID)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return domain.NotFound()
	}
	return nil
}

func (s *Store) LoadAuthSessionForActor(ctx context.Context, p domain.Principal, sid string) (domain.AuthSession, error) {
	var v domain.AuthSession
	e := s.pool.QueryRow(ctx, "SELECT id::text,issuer,subject,actor,org_id,envelope,expires_at,version,revoked_at FROM auth_sessions WHERE id=$1 AND issuer=$2 AND actor=$3 AND org_id=$4 AND revoked_at IS NULL AND expires_at>now()", sid, p.Issuer, actor(p), p.OrgID).Scan(&v.ID, &v.Issuer, &v.Subject, &v.Actor, &v.OrgID, &v.Envelope, &v.ExpiresAt, &v.Version, &v.RevokedAt)
	return v, mapError(e)
}
func (s *Store) ExecutionSummary(ctx context.Context, rid string) (domain.ContextSummary, error) {
	var v domain.ContextSummary
	e := s.pool.QueryRow(ctx, "SELECT c.chat_id::text,c.through_seq,c.text,c.source_hash,c.model_id FROM context_summaries c JOIN runs r ON r.chat_id=c.chat_id JOIN chats live ON live.id=c.chat_id WHERE r.id=$1 AND live.deleted_at IS NULL", rid).Scan(&v.ChatID, &v.ThroughSeq, &v.Text, &v.SourceHash, &v.ModelID)
	if errors.Is(e, pgx.ErrNoRows) {
		return v, nil
	}
	return v, e
}
func (s *Store) ExecutionHistoryAfter(ctx context.Context, rid string, after int64, limit int) ([]domain.ChatEvent, error) {
	r, e := s.ExecutionRun(ctx, rid)
	if e != nil {
		return nil, e
	}
	if limit <= 0 || limit > 1001 {
		limit = 1001
	}
	return s.history(ctx, r.ChatID, after, 0, limit, false)
}
func (s *Store) SaveSummary(ctx context.Context, rid, owner string, fence int64, v domain.ContextSummary) error {
	if v.Text == "" || len(v.Text) > 65536 || v.ThroughSeq <= 0 || len(v.SourceHash) != 64 {
		return domain.NewError(422, "invalid_summary")
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		r, e := leaseRun(ctx, tx, rid, owner, fence)
		if e != nil {
			return e
		}
		if v.ChatID != "" && v.ChatID != r.ChatID {
			return domain.Forbidden()
		}
		var maxSeq int64
		if e = tx.QueryRow(ctx, "SELECT next_seq-1 FROM chats WHERE id=$1 AND deleted_at IS NULL", r.ChatID).Scan(&maxSeq); e != nil {
			return e
		}
		if v.ThroughSeq > maxSeq {
			return domain.NewError(422, "invalid_summary_boundary")
		}
		_, e = tx.Exec(ctx, "INSERT INTO context_summaries(chat_id,through_seq,text,source_hash,model_id) VALUES($1,$2,$3,$4,$5) ON CONFLICT(chat_id) DO UPDATE SET through_seq=EXCLUDED.through_seq,text=EXCLUDED.text,source_hash=EXCLUDED.source_hash,model_id=EXCLUDED.model_id,created_at=now() WHERE context_summaries.through_seq<EXCLUDED.through_seq", r.ChatID, v.ThroughSeq, v.Text, v.SourceHash, v.ModelID)
		return e
	})
}

// RememberServiceIdentity is called only for a trusted deployment manifest binding.
func (s *Store) RememberServiceIdentity(ctx context.Context, issuer, subject, audience string) error {
	if issuer == "" || subject == "" || audience == "" {
		return domain.NewError(422, "invalid_service_identity")
	}
	_, e := s.pool.Exec(ctx, "INSERT INTO service_identities(issuer,subject,audience) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", issuer, subject, audience)
	return e
}
func (s *Store) IsKnownServiceIdentity(ctx context.Context, issuer, subject, audience string) (bool, error) {
	var known bool
	e := s.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM service_identities WHERE issuer=$1 AND subject=$2 AND audience=$3)", issuer, subject, audience).Scan(&known)
	return known, e
}
