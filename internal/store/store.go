package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // Register the database/sql driver used by migrations.
	"github.com/pressly/goose/v3"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/store/dbgen"
	"github.com/complynx/loom/internal/telemetry"
	"github.com/complynx/loom/migrations"
)

type Enqueue func(context.Context, pgx.Tx, string) error
type Store struct {
	semanticEnabled                                bool
	allowedProviders                               map[string]bool
	userCeiling, totalCeiling                      int
	executionDefaults                              domain.Settings
	scriptCeiling, parallelCeiling, contextCeiling int
	pool                                           *pgxpool.Pool
	enqueue                                        Enqueue
	approval                                       func(context.Context, pgx.Tx, domain.Run, domain.RunInput) error
}

func New(pool *pgxpool.Pool, enqueue Enqueue) *Store { return &Store{pool: pool, enqueue: enqueue} }
func (s *Store) Pool() *pgxpool.Pool                 { return s.pool }
func Migrate(ctx context.Context, dsn string) (err error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := db.Close(); err == nil {
			err = closeErr
		}
	}()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.Files)
	if err != nil {
		return err
	}
	_, err = p.Up(ctx)
	return err
}
func id() string            { return uuid.NewString() }
func document(v any) []byte { b, _ := json.Marshal(v); return b }
func hash(v any) string     { h := sha256.Sum256(document(v)); return hex.EncodeToString(h[:]) }
func actor(p domain.Principal) string {
	if p.Actor != "" {
		return p.Actor
	}
	return p.Subject
}
func scope(p domain.Principal, route string) string {
	return p.Issuer + "\x1f" + p.Subject + "\x1f" + actor(p) + "\x1f" + p.OrgID + "\x1f" + route
}
func mapError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NotFound()
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "22P02" {
		return domain.NewError(http.StatusUnprocessableEntity, "invalid_id")
	}
	if errors.As(err, &pe) && pe.Code == "23505" {
		if pe.ConstraintName == "passive_event_receipts_pkey" {
			return domain.Conflict("event_conflict")
		}
		if pe.ConstraintName == "one_active_run_per_chat" {
			return domain.Conflict("chat_busy")
		}
		return domain.Conflict("conflict")
	}
	return err
}

// tx retries confirmed database aborts only. Callbacks must perform database work only.
func (s *Store) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	for attempt := range transactionAttempts {
		err := s.txOnce(ctx, fn)
		var postgres *pgconn.PgError
		if !errors.As(err, &postgres) || (postgres.Code != "40P01" && postgres.Code != "55P03") {
			return err
		}
		if attempt == transactionAttempts-1 {
			return &domain.Error{
				Status:    http.StatusServiceUnavailable,
				Code:      "database_busy",
				Message:   "database_busy",
				Retryable: true,
			}
		}
		if cancelErr := ctx.Err(); cancelErr != nil {
			return cancelErr
		}
	}
	return domain.Conflict("transaction_conflict")
}
func (s *Store) txOnce(ctx context.Context, fn func(pgx.Tx) error) (err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), transactionCleanupTimeout)
		defer cancel()
		rollbackErr := tx.Rollback(cleanup)
		if rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) && err == nil {
			err = rollbackErr
		}
	}()
	if err = fn(tx); err != nil {
		return mapError(err)
	}
	return mapError(tx.Commit(ctx))
}
func (s *Store) Bootstrap(ctx context.Context, settings domain.Settings) error {
	settings = s.normalizeExecutionSettings(normalizeSettings(settings))
	if err := validateSettingsModels(settings); err != nil {
		return err
	}
	if e := s.validateProviders(settings); e != nil {
		return e
	}
	if e := validateLifecycleSettings(settings); e != nil {
		return e
	}
	settings.Version = 1
	_, err := s.pool.Exec(
		ctx,
		"INSERT INTO settings(singleton,doc,version) VALUES(true,$1,1) ON CONFLICT DO NOTHING",
		document(settings),
	)
	return err
}
func (s *Store) GetSettings(ctx context.Context) (domain.Settings, error) {
	var v domain.Settings
	row, e := dbgen.New(s.pool).ReadSettings(ctx)
	if e != nil {
		return v, mapError(e)
	}
	e = json.Unmarshal(row.Doc, &v)
	v.Version = row.Version
	return s.normalizeExecutionSettings(v), e
}
func (s *Store) ensureUser(ctx context.Context, tx pgx.Tx, p domain.Principal) (string, error) {
	if p.Issuer == "" || p.Subject == "" || p.OrgID == "" {
		return "", domain.NewError(http.StatusUnauthorized, "invalid_identity")
	}
	uid := id()
	err := tx.QueryRow(ctx, "INSERT INTO users(id,issuer,subject) VALUES($1,$2,$3) ON CONFLICT(issuer,subject) DO UPDATE SET subject=EXCLUDED.subject RETURNING id::text", uid, p.Issuer, p.Subject).
		Scan(&uid)
	if err != nil {
		return "", err
	}
	if _, err = tx.Exec(
		ctx,
		"INSERT INTO memberships(owner_id,org_id) VALUES($1,$2) ON CONFLICT DO NOTHING",
		uid,
		p.OrgID,
	); err != nil {
		return "", err
	}
	var raw []byte
	if err = tx.QueryRow(ctx, "SELECT doc FROM settings WHERE singleton").Scan(&raw); err != nil {
		return "", err
	}
	var settings domain.Settings
	if err = json.Unmarshal(raw, &settings); err != nil {
		return "", err
	}
	_, err = tx.Exec(
		ctx,
		"INSERT INTO user_preferences(owner_id,doc) VALUES($1,$2) ON CONFLICT DO NOTHING",
		uid,
		document(domain.Preferences{ModelID: settings.DefaultModelID, Effort: settings.DefaultEffort, Version: 1}),
	)
	if err != nil {
		return "", err
	}
	tag, err := tx.Exec(
		ctx,
		"INSERT INTO billing_accounts(owner_id,balance) VALUES($1,$2) ON CONFLICT DO NOTHING",
		uid,
		settings.InitialCredits,
	)
	if err != nil {
		return uid, err
	}
	if tag.RowsAffected() == 1 {
		_, err = tx.Exec(
			ctx,
			"INSERT INTO credit_ledger(id,owner_id,settlement_key,amount,kind,reason) VALUES($1,$2,$3,$4,'initial','bootstrap credit policy')",
			id(),
			uid,
			"initial:"+uid,
			settings.InitialCredits,
		)
	}
	return uid, err
}
func (s *Store) ResolveUser(ctx context.Context, p domain.Principal) (string, error) {
	var uid string
	err := s.tx(ctx, func(tx pgx.Tx) error { var err error; uid, err = s.ensureUser(ctx, tx, p); return err })
	return uid, err
}
func (s *Store) defaultChat(ctx context.Context, tx pgx.Tx, uid, org string) (domain.Chat, error) {
	var c domain.Chat
	err := tx.QueryRow(ctx, "INSERT INTO chats(id,owner_id,org_id,is_default) VALUES($1,$2,$3,true) ON CONFLICT(owner_id) WHERE is_default DO UPDATE SET owner_id=EXCLUDED.owner_id RETURNING id::text,owner_id::text,org_id,is_default,created_at", id(), uid, org).
		Scan(&c.ID, &c.OwnerID, &c.OrgID, &c.IsDefault, &c.CreatedAt)
	if err == nil && c.OrgID != org {
		return c, domain.Forbidden()
	}
	return c, err
}
func (s *Store) DefaultChat(ctx context.Context, p domain.Principal) (domain.Chat, error) {
	var c domain.Chat
	err := s.tx(ctx, func(tx pgx.Tx) error {
		uid, e := s.ensureUser(ctx, tx, p)
		if e != nil {
			return e
		}
		c, e = s.defaultChat(ctx, tx, uid, p.OrgID)
		return e
	})
	return c, err
}
func (s *Store) CreateChat(ctx context.Context, p domain.Principal) (domain.Chat, error) {
	var c domain.Chat
	err := s.tx(ctx, func(tx pgx.Tx) error {
		uid, e := s.ensureUser(ctx, tx, p)
		if e != nil {
			return e
		}
		return tx.QueryRow(ctx, "INSERT INTO chats(id,owner_id,org_id) VALUES($1,$2,$3) RETURNING id::text,owner_id::text,org_id,is_default,created_at", id(), uid, p.OrgID).
			Scan(&c.ID, &c.OwnerID, &c.OrgID, &c.IsDefault, &c.CreatedAt)
	})
	return c, err
}
func checkChat(ctx context.Context, tx pgx.Tx, uid, org, cid string) error {
	var ok bool
	err := tx.QueryRow(ctx, "SELECT true FROM chats WHERE id=$1 AND owner_id=$2 AND org_id=$3 AND deleted_at IS NULL FOR SHARE", cid, uid, org).
		Scan(&ok)
	return mapError(err)
}

const runColumns = "doc,state,reason,version,fence,lease_owner,lease_until,pause_requested,cancel_requested,spent_microcredits,reserved_microcredits,created_at,updated_at,active_elapsed_ms+CASE WHEN state='running' AND active_started_at IS NOT NULL THEN floor(greatest(0,extract(epoch FROM (least(clock_timestamp(),COALESCE(lease_until,clock_timestamp()))-active_started_at)))*1000)::bigint ELSE 0 END"

func scanRun(row pgx.Row) (domain.Run, error) {
	var r domain.Run
	var b []byte
	err := row.Scan(
		&b,
		&r.State,
		&r.Reason,
		&r.Version,
		&r.Fence,
		&r.LeaseOwner,
		&r.LeaseUntil,
		&r.PauseRequested,
		&r.CancelRequested,
		&r.SpentMicrocredits,
		&r.ReservedMicrocredits,
		&r.CreatedAt,
		&r.UpdatedAt,
		&r.ActiveMilliseconds,
	)
	if err != nil {
		return r, mapError(err)
	}
	var base domain.Run
	if err = json.Unmarshal(b, &base); err != nil {
		return r, err
	}
	base.State = r.State
	base.Reason = r.Reason
	base.Version = r.Version
	base.Fence = r.Fence
	base.LeaseOwner = r.LeaseOwner
	base.LeaseUntil = r.LeaseUntil
	base.PauseRequested = r.PauseRequested
	base.CancelRequested = r.CancelRequested
	base.SpentMicrocredits = r.SpentMicrocredits
	base.ReservedMicrocredits = r.ReservedMicrocredits
	base.CreatedAt = r.CreatedAt
	base.UpdatedAt = r.UpdatedAt
	base.ActiveMilliseconds = r.ActiveMilliseconds
	return base, nil
}
func runEvent(ctx context.Context, tx pgx.Tx, rid, kind string, payload any) error {
	var seq int64
	err := tx.QueryRow(ctx, "UPDATE runs SET next_seq=next_seq+1,version=version+1,updated_at=now() WHERE id=$1 RETURNING next_seq-1", rid).
		Scan(&seq)
	if err != nil {
		return err
	}
	_, err = tx.Exec(
		ctx,
		"INSERT INTO run_events(run_id,seq,type,data) VALUES($1,$2,$3,$4)",
		rid,
		seq,
		kind,
		document(payload),
	)
	return err
}
func chatEvent(ctx context.Context, tx pgx.Tx, cid, rid, kind, text, actor string, data any) (domain.ChatEvent, error) {
	var e domain.ChatEvent
	e.Type = kind
	e.Text = text
	e.SourceActor = actor
	e.RunID = rid
	e.Data = document(data)
	err := tx.QueryRow(ctx, "UPDATE chats SET next_seq=next_seq+1 WHERE id=$1 RETURNING next_seq-1", cid).Scan(&e.Seq)
	if err != nil {
		return e, err
	}
	err = tx.QueryRow(ctx, "INSERT INTO chat_events(chat_id,seq,kind,text,doc,run_id,source_actor) VALUES($1,$2,$3,$4,$5,NULLIF($6,'')::uuid,$7) RETURNING occurred_at,received_at", cid, e.Seq, kind, text, e.Data, rid, actor).
		Scan(&e.OccurredAt, &e.ReceivedAt)
	return e, err
}
func idem(ctx context.Context, tx pgx.Tx, sc, key string, request any) (string, error) {
	var rid string
	var same bool
	err := tx.QueryRow(ctx, "SELECT resource_id::text,COALESCE(request_doc=$3::jsonb,request_hash=$4) FROM idempotency_records WHERE scope=$1 AND key=$2", sc, key, document(request), hash(request)).
		Scan(&rid, &same)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !same {
		return "", domain.Conflict("idempotency_conflict")
	}
	return rid, nil
}
func saveIdem(ctx context.Context, tx pgx.Tx, sc, key, rid string, request any) error {
	_, err := tx.Exec(
		ctx,
		"INSERT INTO idempotency_records(scope,key,request_hash,resource_id,request_doc) VALUES($1,$2,$3,$4,$5)",
		sc,
		key,
		hash(request),
		rid,
		document(request),
	)
	return err
}

func (s *Store) CreateRun(
	ctx context.Context,
	p domain.Principal,
	key string,
	in domain.CreateRun,
) (domain.Run, error) {
	var r domain.Run
	if key == "" {
		return r, domain.NewError(http.StatusUnprocessableEntity, "idempotency_key_required")
	}
	if !p.Has("runs.create") || !p.Has("chat.read") {
		return r, domain.Forbidden()
	}
	if err := domain.ValidateDiscovery(in.Discovery); err != nil {
		return r, err
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		uid, e := s.ensureUser(ctx, tx, p)
		if e != nil {
			return e
		}
		canonical := in
		if canonical.AutoAuthSession {
			canonical.AuthSessionID = ""
		}
		rid, e := idem(ctx, tx, scope(p, "runs"), key, canonical)
		if e != nil {
			return e
		}
		if rid != "" {
			r, e = scanRun(
				tx.QueryRow(
					ctx,
					"SELECT "+runColumns+" FROM runs WHERE id=$1 AND owner_id=$2 AND org_id=$3 AND chat_id IN(SELECT id FROM chats WHERE deleted_at IS NULL)",
					rid,
					uid,
					p.OrgID,
				),
			)
			return e
		}
		r, e = s.admitRun(ctx, tx, p, uid, in)
		if e != nil {
			return e
		}
		if e = s.persistRun(ctx, tx, p, key, canonical, r); e != nil {
			return e
		}
		r, e = scanRun(tx.QueryRow(ctx, "SELECT "+runColumns+" FROM runs WHERE id=$1", r.ID))
		return e
	})
	return r, err
}
func (s *Store) GetRun(ctx context.Context, p domain.Principal, rid string) (domain.Run, error) {
	if !p.Has("chat.read") {
		return domain.Run{}, domain.Forbidden()
	}
	r, e := scanRun(
		s.pool.QueryRow(
			ctx,
			"SELECT "+runColumns+" FROM runs WHERE id=$1 AND owner_id=(SELECT id FROM users WHERE issuer=$2 AND subject=$3) AND org_id=$4 AND chat_id IN(SELECT id FROM chats WHERE deleted_at IS NULL)",
			rid,
			p.Issuer,
			p.Subject,
			p.OrgID,
		),
	)
	if e != nil {
		return r, e
	}
	r.UnresolvedSteps, e = s.UnresolvedSteps(ctx, rid)
	return r, e
}
func (s *Store) ExecutionRun(ctx context.Context, rid string) (domain.Run, error) {
	return scanRun(s.pool.QueryRow(ctx, "SELECT "+runColumns+" FROM runs WHERE id=$1", rid))
}

func (s *Store) RunEvents(
	ctx context.Context,
	p domain.Principal,
	rid string,
	after int64,
	limit int,
) ([]domain.Event, error) {
	if _, err := s.GetRun(ctx, p, rid); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := s.pool.Query(
		ctx,
		"SELECT seq,type,data,created_at FROM run_events WHERE run_id=$1 AND seq>$2 ORDER BY seq LIMIT $3",
		rid,
		after,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Event{}
	for rows.Next() {
		var e domain.Event
		if err = rows.Scan(&e.Seq, &e.Type, &e.Data, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func allowedEffort(m domain.Model, effort string) bool {
	maximum := len(m.Efforts) - 1
	for i, e := range m.Efforts {
		if e == m.MaxEffort {
			maximum = i
		}
	}
	for i, e := range m.Efforts {
		if e == effort && i <= maximum {
			return true
		}
	}
	return effort == "" && len(m.Efforts) == 0
}
func audit(ctx context.Context, tx pgx.Tx, p domain.Principal, action, target string, data any) error {
	_, err := tx.Exec(
		ctx,
		"INSERT INTO audit_events(id,actor,org_id,action,target,doc) VALUES($1,$2,$3,$4,$5,$6)",
		id(),
		actor(p),
		p.OrgID,
		action,
		target,
		document(data),
	)
	return err
}

func (s *Store) SetApprovalHandler(fn func(context.Context, pgx.Tx, domain.Run, domain.RunInput) error) {
	s.approval = fn
}

func (s *Store) SetPolicyCeilings(perUser, total int) {
	s.userCeiling = perUser
	s.totalCeiling = total
}

// SetAllowedProviders binds editable model descriptors to configured transport aliases before serving.
func (s *Store) SetAllowedProviders(aliases []string) {
	s.allowedProviders = make(map[string]bool, len(aliases))
	for _, alias := range aliases {
		s.allowedProviders[alias] = true
	}
}
func (s *Store) validateProviders(v domain.Settings) error {
	if s.allowedProviders == nil {
		return nil
	}
	for _, m := range v.Models {
		if !s.allowedProviders[m.Provider] {
			return domain.NewError(http.StatusUnprocessableEntity, "unknown_provider")
		}
	}
	return nil
}

func attachRunArtifacts(
	ctx context.Context,
	tx pgx.Tx,
	p domain.Principal,
	uid, cid string,
	artifactIDs []string,
) error {
	var e error
	if len(artifactIDs) > 0 {
		if !p.Has("artifacts.read") {
			return domain.Forbidden()
		}
		seen := map[string]bool{}
		for _, aid := range artifactIDs {
			if seen[aid] {
				continue
			}
			seen[aid] = true
			var found string
			if e = tx.QueryRow(ctx, "SELECT a.id::text FROM artifacts a WHERE a.id=$1 AND a.owner_id=$2 AND a.org_id=$3 AND ((a.deleted_at IS NULL AND a.expires_at>now()) OR (a.source_reference_id IS NOT NULL AND EXISTS(SELECT 1 FROM chat_artifacts ca JOIN chats c ON c.id=ca.chat_id JOIN artifacts attached ON attached.id=ca.artifact_id WHERE attached.source_reference_id=a.source_reference_id AND c.deleted_at IS NULL))) FOR SHARE OF a", aid, uid, p.OrgID).
				Scan(&found); e != nil {
				return domain.NotFound()
			}
			if _, e = tx.Exec(
				ctx,
				"INSERT INTO chat_artifacts(chat_id,artifact_id) VALUES($1,$2) ON CONFLICT DO NOTHING",
				cid,
				aid,
			); e != nil {
				return e
			}
		}
	}

	return nil
}

func readRunSettings(
	ctx context.Context,
	tx pgx.Tx,
	uid, orgID string,
) (domain.Settings, domain.Preferences, domain.UserPolicy, int, error) {
	var e error
	var settings domain.Settings
	var raw []byte
	if e = tx.QueryRow(ctx, "SELECT doc FROM settings WHERE singleton").Scan(&raw); e != nil {
		return domain.Settings{}, domain.Preferences{}, domain.UserPolicy{}, 0, e
	}
	if e = json.Unmarshal(raw, &settings); e != nil {
		return domain.Settings{}, domain.Preferences{}, domain.UserPolicy{}, 0, e
	}
	var prefs domain.Preferences
	if e = tx.QueryRow(ctx, "SELECT doc FROM user_preferences WHERE owner_id=$1", uid).Scan(&raw); e != nil {
		return domain.Settings{}, domain.Preferences{}, domain.UserPolicy{}, 0, e
	}
	if e = json.Unmarshal(raw, &prefs); e != nil {
		return domain.Settings{}, domain.Preferences{}, domain.UserPolicy{}, 0, e
	}
	limit := settings.ActiveRunLimit
	var policy domain.UserPolicy
	e = tx.QueryRow(ctx, "SELECT doc FROM user_policies WHERE owner_id=$1 AND org_id=$2", uid, orgID).Scan(&raw)
	if e == nil {
		if e = json.Unmarshal(raw, &policy); e != nil {
			return domain.Settings{}, domain.Preferences{}, domain.UserPolicy{}, 0, e
		}
		limit = policy.ActiveRunLimit
		limit = min(limit, settings.ActiveRunLimit)
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return domain.Settings{}, domain.Preferences{}, domain.UserPolicy{}, 0, e
	}

	return settings, prefs, policy, limit, nil
}

func (s *Store) checkRunCapacity(ctx context.Context, tx pgx.Tx, uid string, limit, totalLimit int) error {
	var e error
	if s.userCeiling > 0 && limit > s.userCeiling {
		limit = s.userCeiling
	}
	if s.totalCeiling > 0 && (totalLimit <= 0 || totalLimit > s.totalCeiling) {
		totalLimit = s.totalCeiling
	}
	if totalLimit > 0 {
		var total int
		if e = tx.QueryRow(ctx, "SELECT count(*) FROM runs WHERE state NOT IN('completed','cancelled','failed')").
			Scan(&total); e != nil {
			return e
		}
		if total >= totalLimit {
			return domain.NewError(http.StatusTooManyRequests, "total_run_limit")
		}
	}
	if _, e = tx.Exec(ctx, "SELECT id FROM users WHERE id=$1 FOR UPDATE", uid); e != nil {
		return e
	}
	var active int
	if e = tx.QueryRow(ctx, "SELECT count(*) FROM runs WHERE owner_id=$1 AND state NOT IN('completed','cancelled','failed')", uid).
		Scan(&active); e != nil {
		return e
	}
	if limit > 0 && active >= limit {
		return domain.NewError(http.StatusTooManyRequests, "active_run_limit")
	}

	return nil
}

func buildRun(
	p domain.Principal,
	uid, cid string,
	in domain.CreateRun,
	settings domain.Settings,
	prefs domain.Preferences,
	policy domain.UserPolicy,
) (domain.Run, error) {
	models := settings.Models
	if policy.Models != nil {
		models = effectiveModels(settings.Models, policy.Models)
	}
	found := false
	var selected domain.Model
	for _, m := range models {
		if m.ID == prefs.ModelID && allowedEffort(m, prefs.Effort) {
			found = true
			selected = m
		}
	}
	if !found {
		return domain.Run{}, domain.Forbidden()
	}
	budget := settings.MaxRunMicrocredits
	if in.Budget.MaxMicrocredits > 0 && in.Budget.MaxMicrocredits < budget {
		budget = in.Budget.MaxMicrocredits
	}
	if budget <= 0 {
		return domain.Run{}, domain.NewError(http.StatusUnprocessableEntity, "invalid_budget")
	}
	if in.DisconnectPolicy == "" {
		in.DisconnectPolicy = "continue"
	}
	if in.DisconnectPolicy != "continue" && in.DisconnectPolicy != "pause" {
		return domain.Run{}, domain.NewError(http.StatusUnprocessableEntity, "invalid_disconnect_policy")
	}
	if in.BillingAccountID != "" && in.BillingAccountID != uid {
		return domain.Run{}, domain.Forbidden()
	}
	if err := in.Interaction.Reactions.Validate(); err != nil {
		return domain.Run{}, err
	}
	if in.Interaction.ReplyMode != "" && in.Interaction.ReplyMode != domain.ReplyModeProtocol &&
		in.Interaction.ReplyMode != domain.ReplyModeAgent {
		return domain.Run{}, domain.NewError(http.StatusUnprocessableEntity, "unsupported_reply_mode")
	}
	r := domain.Run{
		PreviousRunID:    in.PreviousRunID,
		DisconnectPolicy: in.DisconnectPolicy,
		Model:            selected,
		SettingsSnapshot: settings,
		ID:               id(),
		ChatID:           cid,
		OwnerID:          uid,
		ActorID:          actor(p),
		OrgID:            p.OrgID,
		State:            domain.Queued,
		Input:            in.Input,
		Discovery:        in.Discovery,
		ModelID:          prefs.ModelID,
		Effort:           prefs.Effort,
		Interaction:      in.Interaction,
		AuthSessionID:    in.AuthSessionID,
		MaxMicrocredits:  budget,
	}

	if in.OutputPolicy != nil {
		r.OutputPolicy = *in.OutputPolicy
	}
	return r, nil
}

func (s *Store) admitRun(
	ctx context.Context,
	tx pgx.Tx,
	p domain.Principal,
	uid string,
	in domain.CreateRun,
) (domain.Run, error) {
	var e error
	cid := in.ChatID
	if cid == "" {
		c, chatErr := s.defaultChat(ctx, tx, uid, p.OrgID)
		if chatErr != nil {
			return domain.Run{}, chatErr
		}
		cid = c.ID
	}
	if e = checkChat(ctx, tx, uid, p.OrgID, cid); e != nil {
		return domain.Run{}, e
	}
	if in.PreviousRunID != "" {
		if _, parseErr := uuid.Parse(in.PreviousRunID); parseErr != nil {
			return domain.Run{}, domain.NewError(http.StatusUnprocessableEntity, "invalid_previous_run_id")
		}
		var previousState string
		e = tx.QueryRow(ctx, "SELECT state FROM runs WHERE id=$1 AND chat_id=$2 AND owner_id=$3 AND org_id=$4", in.PreviousRunID, cid, uid, p.OrgID).
			Scan(&previousState)
		if e != nil {
			return domain.Run{}, mapError(e)
		}
		if previousState != string(domain.Completed) && previousState != string(domain.Cancelled) &&
			previousState != string(domain.Failed) {
			return domain.Run{}, domain.Conflict("previous_run_active")
		}
	}
	if e = attachRunArtifacts(ctx, tx, p, uid, cid, in.Input.ArtifactIDs); e != nil {
		return domain.Run{}, e
	}
	var busy bool
	if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM runs WHERE chat_id=$1 AND state NOT IN('completed','cancelled','failed'))", cid).
		Scan(&busy); e != nil {
		return domain.Run{}, e
	}
	if busy {
		return domain.Run{}, domain.Conflict("chat_busy")
	}

	if in.AuthSessionID != "" {
		var ok bool
		e = tx.QueryRow(ctx, "SELECT true FROM auth_sessions WHERE id=$1 AND issuer=$2 AND subject=$3 AND actor=$4 AND org_id=$5 AND revoked_at IS NULL AND expires_at>now()", in.AuthSessionID, p.Issuer, p.Subject, actor(p), p.OrgID).
			Scan(&ok)
		if e != nil {
			return domain.Run{}, domain.NewError(http.StatusUnauthorized, "invalid_auth_session")
		}
	}
	settings, prefs, policy, e := s.admissionSettings(ctx, tx, uid, p.OrgID)
	if e != nil {
		return domain.Run{}, e
	}
	return buildRun(p, uid, cid, in, settings, prefs, policy)
}

func (s *Store) persistRun(
	ctx context.Context,
	tx pgx.Tx,
	p domain.Principal,
	key string,
	canonical domain.CreateRun,
	r domain.Run,
) error {
	var e error
	in := canonical
	_, e = tx.Exec(
		ctx,
		"INSERT INTO runs(id,chat_id,owner_id,actor_id,org_id,state,doc,max_microcredits,trace_origin) VALUES($1,$2,$3,$4,$5,'queued',$6,$7,$8)",
		r.ID,
		r.ChatID,
		r.OwnerID,
		r.ActorID,
		p.OrgID,
		document(r),
		r.MaxMicrocredits,
		document(telemetry.CaptureTrace(ctx)),
	)
	if e != nil {
		return e
	}
	if _, e = chatEvent(ctx, tx, r.ChatID, r.ID, "message.user", in.Input.Text, r.ActorID, in.Input); e != nil {
		return e
	}
	if e = runEvent(ctx, tx, r.ID, "run.status", map[string]any{eventStatusKey: domain.Queued}); e != nil {
		return e
	}
	if e = saveIdem(ctx, tx, scope(p, "runs"), key, r.ID, canonical); e != nil {
		return e
	}
	if s.enqueue != nil {
		if e = s.enqueue(ctx, tx, r.ID); e != nil {
			return e
		}
	}

	return nil
}

func (s *Store) SetSemanticSearchEnabled(enabled bool) { s.semanticEnabled = enabled }
func (s *Store) SemanticSearchEnabled() bool           { return s != nil && s.semanticEnabled }

// lockAdmission orders settings changes and new admission on all API replicas.
func lockAdmission(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(1129076785)")
	return err
}

func (s *Store) admissionSettings(
	ctx context.Context,
	tx pgx.Tx,
	uid, orgID string,
) (domain.Settings, domain.Preferences, domain.UserPolicy, error) {
	if err := lockAdmission(ctx, tx); err != nil {
		return domain.Settings{}, domain.Preferences{}, domain.UserPolicy{}, err
	}
	settings, prefs, policy, limit, err := readRunSettings(ctx, tx, uid, orgID)
	if err != nil {
		return settings, prefs, policy, err
	}
	settings = s.normalizeExecutionSettings(settings)
	err = s.checkRunCapacity(ctx, tx, uid, limit, settings.TotalActiveRunLimit)
	return settings, prefs, policy, err
}
