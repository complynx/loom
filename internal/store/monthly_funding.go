package store

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/complynx/loom/internal/domain"
)

func fundingEnabled(d domain.MonthlyFundingDefaults, p domain.MonthlyFundingPolicy, service *bool) bool {
	if !d.Enabled {
		return false
	}
	if p.Enabled != nil {
		return *p.Enabled
	}
	return service != nil && !*service
}
func fundingPolicy(ctx context.Context, tx pgx.Tx, uid string) (domain.MonthlyFundingPolicy, error) {
	var p domain.MonthlyFundingPolicy
	var raw []byte
	var version int64
	err := tx.QueryRow(ctx, "SELECT doc,version FROM monthly_funding_policies WHERE owner_id=$1 FOR SHARE", uid).
		Scan(&raw, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(raw, &p)
	p.Version = version
	return p, err
}
func fundingSettings(ctx context.Context, tx pgx.Tx) (domain.Settings, error) {
	var settings domain.Settings
	var raw []byte
	var version int64
	err := tx.QueryRow(ctx, "SELECT doc,version FROM settings WHERE singleton FOR SHARE").Scan(&raw, &version)
	if err != nil {
		return settings, err
	}
	err = json.Unmarshal(raw, &settings)
	settings.Version = version
	return settings, err
}
func fundingPeriod(now time.Time) time.Time {
	now = now.UTC()
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// refillMonthlyTx serializes against reserve, settlement and manual adjustment on the same wallet.
// Existing-wallet callers lock owner, settings, then wallet; new wallets already own the user row.
func refillMonthlyTx(ctx context.Context, tx pgx.Tx, uid string, at *time.Time, settings domain.Settings) error {
	d := domain.FundingDefaults(settings.MonthlyFunding)
	if !d.Enabled {
		return nil
	}
	var balance int64
	var service *bool
	if err := tx.QueryRow(ctx, "SELECT balance,monthly_service_account FROM billing_accounts WHERE owner_id=$1 FOR UPDATE", uid).
		Scan(&balance, &service); err != nil {
		return err
	}
	var now time.Time
	if at != nil {
		now = *at
	} else if err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return err
	}
	p, err := fundingPolicy(ctx, tx, uid)
	if err != nil {
		return err
	}
	if !fundingEnabled(d, p, service) {
		return nil
	}
	period := fundingPeriod(now)
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM monthly_funding_receipts WHERE owner_id=$1 AND period=$2)", uid, period).
		Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	target, err := domain.FundingTarget(d, p)
	if err != nil {
		return err
	}
	credit := max(int64(0), target-balance)
	reason := "monthly_target"
	// A pre-activation initial grant already funded this month. Do not refill spent initial credit.
	var initial bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM credit_ledger WHERE owner_id=$1 AND kind='initial' AND amount>0 AND created_at>=$2 AND created_at<$3)", uid, period, period.AddDate(0, 1, 0)).
		Scan(&initial); err != nil {
		return err
	}
	if initial {
		credit = 0
		reason = "initial_credit_period"
	}
	receipt := domain.MonthlyFundingReceipt{
		Period:               period.Format("2006-01"),
		TargetMicrocredits:   target,
		BalanceMicrocredits:  balance,
		CreditedMicrocredits: credit,
		Defaults:             d,
		Policy:               p,
		SettingsVersion:      settings.Version,
		Reason:               reason,
		CreatedAt:            now.UTC(),
	}
	if _, err = tx.Exec(
		ctx,
		"INSERT INTO monthly_funding_receipts(owner_id,period,doc) VALUES($1,$2,$3)",
		uid,
		period,
		document(receipt),
	); err != nil {
		return err
	}
	if _, err = tx.Exec(
		ctx,
		"UPDATE billing_accounts SET balance=balance+$2 WHERE owner_id=$1",
		uid,
		credit,
	); err != nil {
		return err
	}
	_, err = tx.Exec(
		ctx,
		"INSERT INTO credit_ledger(id,owner_id,settlement_key,amount,kind,reason) VALUES($1,$2,$3,$4,'monthly_topup',$5)",
		id(),
		uid,
		"monthly:"+uid+":"+receipt.Period,
		credit,
		reason,
	)
	return err
}

// RefillMonthly processes current UTC month only, including inactive wallets. Concurrent sweepers are safe.
func (s *Store) RefillMonthly(ctx context.Context, batch int) (int, error) {
	return s.refillMonthly(ctx, nil, batch)
}
func (s *Store) refillMonthlyAt(ctx context.Context, now time.Time, batch int) (int, error) {
	return s.refillMonthly(ctx, &now, batch)
}
func (s *Store) refillMonthly(ctx context.Context, at *time.Time, batch int) (int, error) {
	if batch < 1 || batch > 1000 {
		return 0, domain.NewError(http.StatusUnprocessableEntity, "invalid_batch")
	}
	settings, err := s.GetSettings(ctx)
	if err != nil || !domain.FundingDefaults(settings.MonthlyFunding).Enabled {
		return 0, err
	}
	var now time.Time
	if at != nil {
		now = *at
	} else if err = s.pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return 0, err
	}
	rows, err := s.pool.Query(ctx, `SELECT b.owner_id::text FROM billing_accounts b
 LEFT JOIN monthly_funding_policies p ON p.owner_id=b.owner_id
 WHERE COALESCE((p.doc->>'enabled')::boolean,NOT b.monthly_service_account,false)
 AND NOT EXISTS(SELECT 1 FROM monthly_funding_receipts r WHERE r.owner_id=b.owner_id AND r.period=$1)
 ORDER BY b.owner_id LIMIT $2`, fundingPeriod(now), batch)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var uid string
		if err = rows.Scan(&uid); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, uid)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, err
	}
	for i, uid := range ids {
		err = s.tx(ctx, func(tx pgx.Tx) error {
			if e := lockFundingOwner(ctx, tx, uid); e != nil {
				return e
			}
			current, e := fundingSettings(ctx, tx)
			if e != nil {
				return e
			}
			return refillMonthlyTx(ctx, tx, uid, at, current)
		})
		if err != nil {
			return i, err
		}
	}
	return len(ids), nil
}

// MonthlyFunding is privileged and owner-scoped, matching the existing wallet adjustment authority.
func (s *Store) MonthlyFunding(
	ctx context.Context,
	p domain.Principal,
	uid string,
) (domain.MonthlyFundingStatus, error) {
	if !p.Has("billing.adjust") {
		return domain.MonthlyFundingStatus{}, domain.Forbidden()
	}
	if err := s.targetUser(ctx, p, uid); err != nil {
		return domain.MonthlyFundingStatus{}, err
	}
	return s.monthlyFundingStatus(ctx, uid)
}
func (s *Store) monthlyFundingStatus(ctx context.Context, uid string) (domain.MonthlyFundingStatus, error) {
	var out domain.MonthlyFundingStatus
	err := s.tx(ctx, func(tx pgx.Tx) error {
		settings, e := fundingSettings(ctx, tx)
		if e != nil {
			return e
		}
		var service *bool
		if e = tx.QueryRow(ctx, "SELECT monthly_service_account FROM billing_accounts WHERE owner_id=$1", uid).
			Scan(&service); e != nil {
			return e
		}
		p, e := fundingPolicy(ctx, tx, uid)
		if e != nil {
			return e
		}
		out.Defaults = domain.FundingDefaults(settings.MonthlyFunding)
		out.Policy = p
		out.Enabled = fundingEnabled(out.Defaults, p, service)
		out.Classification = "unknown"
		if service != nil {
			out.Classification = "user"
			if *service {
				out.Classification = "service_account"
			}
		}
		out.TargetMicrocredits, e = domain.FundingTarget(out.Defaults, p)
		if e != nil {
			return e
		}
		out.LastReceipt, e = lastFundingReceipt(ctx, tx, uid)
		if errors.Is(e, domain.ErrNotFound) {
			e = nil
		}
		if e != nil {
			return e
		}
		if out.Enabled {
			out.NextRefillAt, e = nextFundingTime(ctx, tx, out.LastReceipt)
		}
		return e
	})
	return out, err
}

func (s *Store) UpdateMonthlyFunding(
	ctx context.Context,
	p domain.Principal,
	uid string,
	policy domain.MonthlyFundingPolicy,
) (domain.MonthlyFundingStatus, error) {
	if !p.Has("billing.adjust") {
		return domain.MonthlyFundingStatus{}, domain.Forbidden()
	}
	if err := s.targetUser(ctx, p, uid); err != nil {
		return domain.MonthlyFundingStatus{}, err
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if e := lockFundingOwner(ctx, tx, uid); e != nil {
			return e
		}
		settings, e := fundingSettings(ctx, tx)
		if e != nil {
			return e
		}
		if _, e = domain.FundingTarget(domain.FundingDefaults(settings.MonthlyFunding), policy); e != nil {
			return e
		}
		return saveFundingPolicy(ctx, tx, p, uid, policy)
	})
	if err != nil {
		return domain.MonthlyFundingStatus{}, err
	}
	return s.monthlyFundingStatus(ctx, uid)
}
func validateFundingPolicies(ctx context.Context, tx pgx.Tx, settings domain.Settings) error {
	d := domain.FundingDefaults(settings.MonthlyFunding)
	if _, err := domain.FundingTarget(d, domain.MonthlyFundingPolicy{}); err != nil {
		return err
	}
	var invalid bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM monthly_funding_policies WHERE
 floor(COALESCE((doc->>'base_microcredits')::numeric,$1::numeric)*$2::numeric*COALESCE((doc->>'factor_ppm')::numeric,1000000)/1000000000000)>9223372036854775807)`, d.BaseMicrocredits, d.FactorPPM).Scan(&invalid)
	if err != nil {
		return err
	}
	if invalid {
		return domain.NewError(http.StatusUnprocessableEntity, "monthly_funding_overflow")
	}
	return nil
}

func lastFundingReceipt(ctx context.Context, tx pgx.Tx, uid string) (*domain.MonthlyFundingReceipt, error) {
	var raw []byte
	err := tx.QueryRow(ctx, "SELECT doc FROM monthly_funding_receipts WHERE owner_id=$1 ORDER BY period DESC LIMIT 1", uid).
		Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.NotFound()
	}
	if err != nil {
		return nil, err
	}
	var receipt domain.MonthlyFundingReceipt
	err = json.Unmarshal(raw, &receipt)
	return &receipt, err
}
func nextFundingTime(ctx context.Context, tx pgx.Tx, receipt *domain.MonthlyFundingReceipt) (*time.Time, error) {
	var now time.Time
	if err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return nil, err
	}
	next := now.UTC()
	if receipt != nil && receipt.Period >= fundingPeriod(now).Format("2006-01") {
		period, err := time.Parse("2006-01", receipt.Period)
		if err != nil {
			return nil, err
		}
		next = period.AddDate(0, 1, 0)
	}
	return &next, nil
}
func lockFundingSettingsUpdate(ctx context.Context, tx pgx.Tx, settings domain.Settings) error {
	if _, err := tx.Exec(ctx, "SELECT singleton FROM settings WHERE singleton FOR UPDATE"); err != nil {
		return err
	}
	return validateFundingPolicies(ctx, tx, settings)
}

func (s *Store) UpdateMonthlyFundingDefaults(
	ctx context.Context,
	p domain.Principal,
	in domain.MonthlyFundingConfiguration,
) (domain.MonthlyFundingConfiguration, error) {
	if !p.Has("settings.write") {
		return in, domain.Forbidden()
	}
	settings, err := s.GetSettings(ctx)
	if err != nil {
		return in, err
	}
	settings.MonthlyFunding = &in.Defaults
	settings, err = s.UpdateSettings(ctx, p, in.Version, settings)
	return domain.MonthlyFundingConfiguration{
		Defaults: domain.FundingDefaults(settings.MonthlyFunding),
		Version:  settings.Version,
	}, err
}

func saveFundingPolicy(
	ctx context.Context,
	tx pgx.Tx,
	p domain.Principal,
	uid string,
	policy domain.MonthlyFundingPolicy,
) error {
	if policy.Version < 0 || policy.Version == math.MaxInt64 {
		return domain.Conflict("version_conflict")
	}
	version := policy.Version
	policy.Version++
	var tag pgconn.CommandTag
	var err error
	if version == 0 {
		tag, err = tx.Exec(
			ctx,
			"INSERT INTO monthly_funding_policies(owner_id,doc,version) VALUES($1,$2,1) ON CONFLICT DO NOTHING",
			uid,
			document(policy),
		)
	} else {
		tag, err = tx.Exec(
			ctx,
			"UPDATE monthly_funding_policies SET doc=$2,version=version+1 WHERE owner_id=$1 AND version=$3",
			uid,
			document(policy),
			version,
		)
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return domain.Conflict("version_conflict")
	}
	return audit(ctx, tx, p, "billing.monthly_policy.update", uid, policy)
}

// Lock the referenced owner before wallets/settings: receipt/policy foreign keys also acquire this lock.
func lockFundingOwner(ctx context.Context, tx pgx.Tx, uid string) error {
	var found string
	return tx.QueryRow(ctx, "SELECT id::text FROM users WHERE id=$1 FOR KEY SHARE", uid).Scan(&found)
}
