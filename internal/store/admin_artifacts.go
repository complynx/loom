package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/store/dbgen"
)

func (s *Store) Preferences(ctx context.Context, p domain.Principal) (domain.Preferences, error) {
	uid, e := s.ResolveUser(ctx, p)
	if e != nil {
		return domain.Preferences{}, e
	}
	return s.preferences(ctx, uid)
}
func (s *Store) preferences(ctx context.Context, uid string) (domain.Preferences, error) {
	var v domain.Preferences
	var raw []byte
	var version int64
	e := s.pool.QueryRow(ctx, "SELECT doc,version FROM user_preferences WHERE owner_id=$1", uid).Scan(&raw, &version)
	if e != nil {
		return v, mapError(e)
	}
	e = json.Unmarshal(raw, &v)
	v.Version = version
	return v, e
}
func (s *Store) Models(ctx context.Context, p domain.Principal) ([]domain.Model, error) {
	uid, e := s.ResolveUser(ctx, p)
	if e != nil {
		return nil, e
	}
	policy, e := s.policy(ctx, uid, p.OrgID)
	return policy.Models, e
}
func (s *Store) policy(ctx context.Context, uid, org string) (domain.UserPolicy, error) {
	settings, e := s.GetSettings(ctx)
	if e != nil {
		return domain.UserPolicy{}, e
	}
	v := domain.UserPolicy{Models: settings.Models, ActiveRunLimit: settings.ActiveRunLimit}
	var raw []byte
	var version int64
	e = s.pool.QueryRow(ctx, "SELECT doc,version FROM user_policies WHERE owner_id=$1 AND org_id=$2", uid, org).
		Scan(&raw, &version)
	if errors.Is(e, pgx.ErrNoRows) {
		return v, nil
	}
	if e != nil {
		return v, e
	}
	v = domain.UserPolicy{}
	e = json.Unmarshal(raw, &v)
	v.Version = version
	v.Models = effectiveModels(settings.Models, v.Models)
	if v.ActiveRunLimit > settings.ActiveRunLimit {
		v.ActiveRunLimit = settings.ActiveRunLimit
	}
	return v, e
}
func (s *Store) Policy(ctx context.Context, p domain.Principal, uid string) (domain.UserPolicy, error) {
	if !p.Has("users.policy.write") {
		return domain.UserPolicy{}, domain.Forbidden()
	}
	if e := s.targetUser(ctx, p, uid); e != nil {
		return domain.UserPolicy{}, e
	}
	return s.policy(ctx, uid, p.OrgID)
}
func (s *Store) targetUser(ctx context.Context, p domain.Principal, uid string) error {
	var ok bool
	e := s.pool.QueryRow(ctx, "SELECT true FROM memberships WHERE owner_id=$1 AND org_id=$2", uid, p.OrgID).Scan(&ok)
	return mapError(e)
}

func (s *Store) UpdatePreferences(
	ctx context.Context,
	p domain.Principal,
	uid string,
	version int64,
	v domain.Preferences,
) (domain.Preferences, error) {
	uid, userErr := s.preferencesUser(ctx, p, uid)
	if userErr != nil {
		return v, userErr
	}
	policy, e := s.policy(ctx, uid, p.OrgID)
	if e != nil {
		return v, e
	}
	allowed := false
	for _, m := range policy.Models {
		if m.ID == v.ModelID && allowedEffort(m, v.Effort) {
			allowed = true
		}
	}
	if !allowed {
		return v, domain.Forbidden()
	}
	v.Version = version + 1
	e = s.tx(ctx, func(tx pgx.Tx) error {
		tag, updateErr := tx.Exec(
			ctx,
			"UPDATE user_preferences SET doc=$2,version=version+1 WHERE owner_id=$1 AND version=$3",
			uid,
			document(v),
			version,
		)
		if updateErr != nil {
			return updateErr
		}
		if tag.RowsAffected() != 1 {
			return domain.Conflict("version_conflict")
		}
		return audit(ctx, tx, p, "preferences.update", uid, v)
	})
	return v, e
}

func (s *Store) UpdatePolicy(
	ctx context.Context,
	p domain.Principal,
	uid string,
	version int64,
	v domain.UserPolicy,
) (domain.UserPolicy, error) {
	if !p.Has("users.policy.write") {
		return v, domain.Forbidden()
	}
	if e := s.targetUser(ctx, p, uid); e != nil {
		return v, e
	}
	settings, e := s.GetSettings(ctx)
	if e != nil {
		return v, e
	}
	if v.ActiveRunLimit < 1 || v.ActiveRunLimit > settings.ActiveRunLimit {
		return v, domain.NewError(http.StatusUnprocessableEntity, "invalid_policy")
	}
	if validationErr := validatePolicyModels(v, settings); validationErr != nil {
		return v, validationErr
	}
	v.Version = version + 1
	e = s.tx(ctx, func(tx pgx.Tx) error {
		if err := lockAdmission(ctx, tx); err != nil {
			return err
		}
		if version == 0 {
			_, insertErr := tx.Exec(
				ctx,
				"INSERT INTO user_policies(owner_id,org_id,doc,version) VALUES($1,$2,$3,1)",
				uid,
				p.OrgID,
				document(v),
			)
			if insertErr != nil {
				return insertErr
			}
		} else {
			tag, updateErr := tx.Exec(
				ctx,
				"UPDATE user_policies SET doc=$3,version=version+1 WHERE owner_id=$1 AND org_id=$2 AND version=$4",
				uid,
				p.OrgID,
				document(v),
				version,
			)
			if updateErr != nil {
				return updateErr
			}
			if tag.RowsAffected() != 1 {
				return domain.Conflict("version_conflict")
			}
		}
		return audit(ctx, tx, p, "policy.update", uid, v)
	})
	return v, e
}

func (s *Store) UpdateSettings(
	ctx context.Context,
	p domain.Principal,
	version int64,
	v domain.Settings,
) (domain.Settings, error) {
	if !p.Has("settings.write") {
		return v, domain.Forbidden()
	}
	v = s.normalizeExecutionSettings(normalizeSettings(v))
	if e := s.validateExecutionSettings(v); e != nil {
		return v, e
	}
	if e := s.validateProviders(v); e != nil {
		return v, e
	}
	if e := validateLifecycleSettings(v); e != nil {
		return v, e
	}
	if s.userCeiling > 0 && v.ActiveRunLimit > s.userCeiling {
		return v, domain.NewError(http.StatusUnprocessableEntity, "hard_policy_ceiling")
	}
	if v.RegexPatternMaxChars < 1 || v.RegexPatternMaxChars > 4096 || v.RegexScanMaxBytes < 1 ||
		v.RegexScanMaxBytes > 16777216 ||
		v.RegexDeadlineMS < 1 ||
		v.RegexDeadlineMS > 5000 ||
		v.RunActiveTimeSeconds < 1 {
		return v, domain.NewError(http.StatusUnprocessableEntity, "invalid_settings")
	}
	if v.InitialCredits < 0 || v.MaxRunMicrocredits <= 0 || v.ActiveRunLimit <= 0 || v.HistoryRecentLimit <= 0 ||
		v.HistoryRecentLimit > 200 ||
		v.MaxSteps <= 0 ||
		v.QuestionTimeoutSeconds <= 0 {
		return v, domain.NewError(http.StatusUnprocessableEntity, "invalid_settings")
	}
	if validationErr := validateSettingsModels(v); validationErr != nil {
		return v, validationErr
	}
	v.Version = version + 1
	e := s.tx(ctx, func(tx pgx.Tx) error {
		if e := lockAdmission(ctx, tx); e != nil {
			return e
		}
		tag, e := tx.Exec(
			ctx,
			"UPDATE settings SET doc=$1,version=version+1 WHERE singleton AND version=$2",
			document(v),
			version,
		)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return domain.Conflict("version_conflict")
		}
		return audit(ctx, tx, p, "settings.update", "global", v)
	})
	return v, e
}
func (s *Store) Balance(ctx context.Context, p domain.Principal) (domain.Balance, error) {
	uid, e := s.ResolveUser(ctx, p)
	if e != nil {
		return domain.Balance{}, e
	}
	return s.balance(ctx, uid)
}
func (s *Store) balance(ctx context.Context, uid string) (domain.Balance, error) {
	var id pgtype.UUID
	if e := id.Scan(uid); e != nil {
		return domain.Balance{}, domain.NewError(http.StatusUnprocessableEntity, "invalid_id")
	}
	v, e := dbgen.New(s.pool).ReadBalance(ctx, id)
	return domain.Balance{
		AvailableMicrocredits: v.Available,
		SpentMicrocredits:     v.Spent,
		ReservedMicrocredits:  v.Reserved,
	}, mapError(
		e,
	)
}

func (s *Store) AdjustCredits(
	ctx context.Context,
	p domain.Principal,
	uid, key string,
	amount int64,
	reason string,
) (domain.Balance, error) {
	if !p.Has("billing.adjust") {
		return domain.Balance{}, domain.Forbidden()
	}
	if key == "" || reason == "" {
		return domain.Balance{}, domain.NewError(http.StatusUnprocessableEntity, "invalid_adjustment")
	}
	if e := s.targetUser(ctx, p, uid); e != nil {
		return domain.Balance{}, e
	}
	request := struct {
		Amount int64
		Reason string
	}{amount, reason}
	e := s.tx(ctx, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, "SELECT owner_id FROM billing_accounts WHERE owner_id=$1 FOR UPDATE", uid); e != nil {
			return e
		}
		existing, e := idem(ctx, tx, scope(p, "adjust:"+uid), key, request)
		if e != nil {
			return e
		}
		if existing != "" {
			return nil
		}
		tag, e := tx.Exec(
			ctx,
			"UPDATE billing_accounts SET balance=balance+$2 WHERE owner_id=$1 AND balance+$2>=reserved",
			uid,
			amount,
		)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return domain.Conflict("insufficient_balance")
		}
		_, e = tx.Exec(
			ctx,
			"INSERT INTO credit_ledger(id,owner_id,settlement_key,amount,kind,reason) VALUES($1,$2,$3,$4,'adjustment',$5)",
			id(),
			uid,
			scope(p, "adjust:"+uid)+":"+key,
			amount,
			reason,
		)
		if e != nil {
			return e
		}
		if e = saveIdem(ctx, tx, scope(p, "adjust:"+uid), key, uid, request); e != nil {
			return e
		}
		return audit(ctx, tx, p, "billing.adjust", uid, request)
	})
	if e != nil {
		return domain.Balance{}, e
	}
	return s.balance(ctx, uid)
}
func (s *Store) CommitArtifact(ctx context.Context, v domain.Artifact, quota int64) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		return commitArtifact(ctx, tx, v, quota)
	})
}

// CommitChatArtifact inserts imported metadata and its chat reference together.
// The chat lock prevents deletion from leaving an unreferenced live artifact.
func (s *Store) CommitChatArtifact(
	ctx context.Context,
	p domain.Principal,
	cid string,
	v domain.Artifact,
	quota int64,
) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		var uid string
		if e := tx.QueryRow(ctx, "SELECT owner_id::text FROM chats WHERE id=$1 AND owner_id=(SELECT id FROM users WHERE issuer=$2 AND subject=$3) AND org_id=$4 AND deleted_at IS NULL FOR SHARE", cid, p.Issuer, p.Subject, p.OrgID).
			Scan(&uid); e != nil {
			return mapError(e)
		}
		if uid != v.OwnerID || p.OrgID != v.OrgID {
			return domain.Forbidden()
		}
		if e := commitArtifact(ctx, tx, v, quota); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, "INSERT INTO chat_artifacts(chat_id,artifact_id) VALUES($1,$2)", cid, v.ID)
		return e
	})
}

func commitArtifact(ctx context.Context, tx pgx.Tx, v domain.Artifact, quota int64) error {
	if v.Size < 0 || quota <= 0 {
		return domain.NewError(http.StatusUnprocessableEntity, "invalid_artifact")
	}
	var uid string
	if e := tx.QueryRow(ctx, "SELECT id::text FROM users WHERE id=$1 FOR UPDATE", v.OwnerID).Scan(&uid); e != nil {
		return e
	}
	var total int64
	if e := tx.QueryRow(ctx, "SELECT COALESCE(sum(size),0) FROM artifacts WHERE owner_id=$1 AND collected_at IS NULL", v.OwnerID).
		Scan(&total); e != nil {
		return e
	}
	if v.Size > quota-total {
		return domain.NewError(http.StatusTooManyRequests, "artifact_quota")
	}
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}
	if v.ExpiresAt.IsZero() {
		v.ExpiresAt = v.CreatedAt.Add(domain.DefaultArtifactTTL)
	}
	_, e := tx.Exec(
		ctx,
		"INSERT INTO artifacts(id,owner_id,org_id,mime,sha256,size,created_at,external_disclosure_allowed,source_service_id,expires_at,source_external_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)",
		v.ID,
		v.OwnerID,
		v.OrgID,
		v.MIME,
		v.SHA256,
		v.Size,
		v.CreatedAt,
		v.ExternalDisclosureAllowed,
		v.SourceServiceID,
		v.ExpiresAt,
		v.SourceExternalID,
	)
	return e
}
func (s *Store) GetArtifact(ctx context.Context, aid string) (domain.Artifact, error) {
	var id pgtype.UUID
	if e := id.Scan(aid); e != nil {
		return domain.Artifact{}, domain.NewError(http.StatusUnprocessableEntity, "invalid_id")
	}
	v, e := dbgen.New(s.pool).ReadArtifact(ctx, id)
	return domain.Artifact{
		ID:                        v.ID,
		OwnerID:                   v.OwnerID,
		OrgID:                     v.OrgID,
		MIME:                      v.Mime,
		SHA256:                    v.Sha256,
		Size:                      v.Size,
		CreatedAt:                 v.CreatedAt.Time,
		ExternalDisclosureAllowed: v.ExternalDisclosureAllowed,
		SourceServiceID:           v.SourceServiceID,
		ExpiresAt:                 v.ExpiresAt.Time,
		SourceExternalID:          v.SourceExternalID,
	}, mapError(
		e,
	)
}
func (s *Store) ListArtifactIDs(ctx context.Context) ([]string, error) {
	return dbgen.New(s.pool).ListArtifactIDs(ctx)
}

func (s *Store) UserPreferences(ctx context.Context, p domain.Principal, uid string) (domain.Preferences, error) {
	if !p.Has("preferences.other.write") {
		return domain.Preferences{}, domain.Forbidden()
	}
	if e := s.targetUser(ctx, p, uid); e != nil {
		return domain.Preferences{}, e
	}
	return s.preferences(ctx, uid)
}
func (s *Store) CheckRunPolicy(ctx context.Context, rid string) error {
	r, e := s.ExecutionRun(ctx, rid)
	if e != nil {
		return e
	}
	policy, e := s.policy(ctx, r.OwnerID, r.OrgID)
	if e != nil {
		return e
	}
	for _, m := range policy.Models {
		if m.ID == r.ModelID && allowedEffort(m, r.Effort) {
			return nil
		}
	}
	return domain.Forbidden()
}

func (s *Store) ListDeletedArtifacts(ctx context.Context, limit int) ([]domain.Artifact, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, e := s.pool.Query(
		ctx,
		"SELECT id::text,owner_id::text,org_id,mime,sha256,size,created_at,external_disclosure_allowed,source_service_id FROM artifacts WHERE deleted_at IS NOT NULL AND collected_at IS NULL ORDER BY deleted_at,id LIMIT $1",
		limit,
	)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []domain.Artifact{}
	for rows.Next() {
		var v domain.Artifact
		if e = rows.Scan(
			&v.ID,
			&v.OwnerID,
			&v.OrgID,
			&v.MIME,
			&v.SHA256,
			&v.Size,
			&v.CreatedAt,
			&v.ExternalDisclosureAllowed,
			&v.SourceServiceID,
		); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) MarkArtifactCollected(ctx context.Context, aid string) error {
	tag, e := s.pool.Exec(
		ctx,
		"UPDATE artifacts SET collected_at=COALESCE(collected_at,now()) WHERE id=$1 AND deleted_at IS NOT NULL",
		aid,
	)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return domain.NotFound()
	}
	return nil
}

func validatePolicyModels(v domain.UserPolicy, settings domain.Settings) error {
	seenModels := map[string]bool{}
	for _, m := range v.Models {
		if seenModels[m.ID] {
			return domain.NewError(http.StatusUnprocessableEntity, "duplicate_model")
		}
		seenModels[m.ID] = true
		ok := false
		for _, catalog := range settings.Models {
			if m.ID == catalog.ID && (m.MaxEffort == "" || allowedEffort(catalog, m.MaxEffort)) {
				ok = true
			}
		}
		if !ok {
			return domain.NewError(http.StatusUnprocessableEntity, "invalid_model_policy")
		}
	}

	return nil
}

func validateSettingsModels(v domain.Settings) error {
	found := false
	seen := map[string]bool{}
	for _, m := range v.Models {
		if seen[m.ID] || m.ID == "" || m.ProviderModel == "" || m.MaxOutputTokens <= 0 ||
			m.InputMicrocreditsPerMillion < 0 ||
			m.InputCachedMicrocreditsPerMillion < 0 ||
			m.OutputMicrocreditsPerMillion < 0 ||
			domain.ValidateTariff(m) != nil ||
			domain.ValidateCompaction(m) != nil {
			return domain.NewError(http.StatusUnprocessableEntity, "invalid_model")
		}
		seen[m.ID] = true
		if m.ID == v.DefaultModelID && allowedEffort(m, v.DefaultEffort) {
			found = true
		}
	}
	if !found {
		return domain.NewError(http.StatusUnprocessableEntity, "invalid_default_model")
	}

	return nil
}

func (s *Store) preferencesUser(ctx context.Context, p domain.Principal, uid string) (string, error) {
	if uid == "" {
		if !p.Has("preferences.self.write") {
			return "", domain.Forbidden()
		}
		return s.ResolveUser(ctx, p)
	}
	if !p.Has("preferences.other.write") {
		return "", domain.Forbidden()
	}
	if err := s.targetUser(ctx, p, uid); err != nil {
		return "", err
	}
	return uid, nil
}
