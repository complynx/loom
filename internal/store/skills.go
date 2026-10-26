package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"

	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
)

var skillName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func validSkillName(name string) bool { return len(name) <= 64 && skillName.MatchString(name) }
func invalidSkill() error             { return domain.NewError(http.StatusUnprocessableEntity, "invalid_skill") }

func skillAuthority(p domain.Principal, write bool) error {
	if p.OrgID == "" || p.Subject == "" || !p.Has("chat.read") || (write && !p.Has(domain.SkillsWritePermission)) {
		return domain.Forbidden()
	}
	return nil
}

// ListSkills returns usecases only; full procedures are loaded by name.
func (s *Store) ListSkills(
	ctx context.Context,
	p domain.Principal,
	in domain.SkillListRequest,
) (domain.SkillListResult, error) {
	out := domain.SkillListResult{Skills: []domain.Skill{}}
	if err := skillAuthority(p, false); err != nil {
		return out, err
	}
	if !memoryText(in.Query, memoryQueryLimit) || in.Limit < 0 || in.Limit > domain.MaxOrganizationSkills ||
		(in.AfterName != "" && !validSkillName(in.AfterName)) {
		return out, invalidSkill()
	}
	if in.Limit == 0 {
		in.Limit = 20
	}
	rows, err := s.pool.Query(
		ctx,
		`SELECT name,usecase,require,version FROM skills WHERE org_id=$1 AND NOT deleted AND name>$2 AND ($3='' OR strpos(lower(name || ' ' || usecase),lower($3))>0) ORDER BY name LIMIT $4`,
		p.OrgID,
		in.AfterName,
		in.Query,
		in.Limit+1,
	)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var skill domain.Skill
		if err = rows.Scan(&skill.Name, &skill.Usecase, &skill.Require, &skill.Version); err != nil {
			return out, err
		}
		if len(out.Skills) == in.Limit {
			out.Partial = true
			out.NextAfterName = out.Skills[len(out.Skills)-1].Name
			break
		}
		out.Skills = append(out.Skills, skill)
	}
	return out, rows.Err()
}

func (s *Store) ReadSkill(ctx context.Context, p domain.Principal, name string) (domain.Skill, error) {
	out := domain.Skill{}
	if err := skillAuthority(p, false); err != nil {
		return out, err
	}
	if !validSkillName(name) {
		return out, invalidSkill()
	}
	err := s.pool.QueryRow(ctx, `SELECT name,usecase,body,require,version FROM skills WHERE org_id=$1 AND name=$2 AND NOT deleted`, p.OrgID, name).
		Scan(&out.Name, &out.Usecase, &out.Body, &out.Require, &out.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, domain.NotFound()
	}
	return out, err
}

// PutSkill is a compare-and-swap. Version zero creates or recreates a deleted skill.
func (s *Store) PutSkill(
	ctx context.Context,
	p domain.Principal,
	name string,
	in domain.SkillWrite,
) (domain.Skill, error) {
	out := domain.Skill{}
	if err := skillAuthority(p, true); err != nil {
		return out, err
	}
	if in.Version < 0 || !domain.ValidSkill(name, in.Usecase, in.Body) || !domain.ValidSkillRequirements(in.Require) {
		return out, invalidSkill()
	}
	if in.Require == nil {
		in.Require = []string{}
	}
	requirements, err := json.Marshal(in.Require)
	if err != nil {
		return out, err
	}
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if in.Version == 0 {
			if lockErr := lockSkillCreation(ctx, tx, p.OrgID, name); lockErr != nil {
				return lockErr
			}
		}
		query := `INSERT INTO skills(org_id,name,usecase,body,require,version) VALUES($1,$2,$3,$4,$6,1)
 ON CONFLICT(org_id,name) DO UPDATE SET usecase=$3,body=$4,require=$6,version=skills.version+1,deleted=false
 WHERE skills.deleted AND $5::bigint=0
 RETURNING name,usecase,body,require,version`
		if in.Version > 0 {
			query = `UPDATE skills SET usecase=$3,body=$4,require=$6,version=version+1 WHERE org_id=$1 AND name=$2 AND version=$5 AND NOT deleted RETURNING name,usecase,body,require,version`
		}
		writeErr := tx.QueryRow(ctx, query, p.OrgID, name, in.Usecase, in.Body, in.Version, requirements).
			Scan(&out.Name, &out.Usecase, &out.Body, &out.Require, &out.Version)
		if errors.Is(writeErr, pgx.ErrNoRows) {
			return domain.Conflict("skill_version_conflict")
		}
		return writeErr
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return out, domain.Conflict("skill_version_conflict")
	}
	return out, err
}

func (s *Store) DeleteSkill(ctx context.Context, p domain.Principal, name string, version int64) error {
	if err := skillAuthority(p, true); err != nil {
		return err
	}
	if !validSkillName(name) || version < 1 {
		return invalidSkill()
	}
	result, err := s.pool.Exec(
		ctx,
		`UPDATE skills SET usecase='',body='',require='[]',deleted=true,version=version+1 WHERE org_id=$1 AND name=$2 AND version=$3 AND NOT deleted`,
		p.OrgID,
		name,
		version,
	)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return domain.Conflict("skill_version_conflict")
	}
	return nil
}

func lockSkillCreation(ctx context.Context, tx pgx.Tx, org, name string) error {
	if _, err := tx.Exec(
		ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('loom.skills:' || $1,0))`,
		org,
	); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM skills WHERE org_id=$1 AND name=$2 AND NOT deleted)`, org, name).
		Scan(&exists); err != nil {
		return err
	}
	if exists {
		return domain.Conflict("skill_version_conflict")
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM skills WHERE org_id=$1 AND NOT deleted`, org).
		Scan(&count); err != nil {
		return err
	}
	if count >= domain.MaxOrganizationSkills {
		return domain.NewError(http.StatusUnprocessableEntity, "skill_limit")
	}
	return nil
}
