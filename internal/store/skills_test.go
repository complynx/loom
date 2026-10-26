package store_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
)

func TestSkillsLifecycleAuthorityAndOrganization(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	p := principal("skill-editor")
	input := domain.SkillWrite{
		Usecase: "Inspect documents",
		Body:    "Read pages and cite evidence.",
		Require: []string{"files.pdf_text"},
	}
	_, err := s.PutSkill(t.Context(), p, "inspect-document", input)
	code(t, err, "forbidden")
	p.Permissions[domain.SkillsWritePermission] = true
	skill, err := s.PutSkill(t.Context(), p, "inspect-document", input)
	require.NoError(t, err)
	require.EqualValues(t, 1, skill.Version)
	_, err = s.PutSkill(t.Context(), p, skill.Name, input)
	code(t, err, "skill_version_conflict")
	reader := principal("skill-reader")
	read, err := s.ReadSkill(t.Context(), reader, skill.Name)
	require.NoError(t, err)
	require.Equal(t, input.Body, read.Body)
	require.Equal(t, input.Require, read.Require)
	page, err := s.ListSkills(t.Context(), reader, domain.SkillListRequest{Query: "DOCUMENT"})
	require.NoError(t, err)
	require.Len(t, page.Skills, 1)
	require.Empty(t, page.Skills[0].Body)
	require.Equal(t, input.Require, page.Skills[0].Require)
	reader.OrgID = "other-org"
	_, err = s.ReadSkill(t.Context(), reader, skill.Name)
	code(t, err, "not_found")
	require.NoError(t, s.DeleteSkill(t.Context(), p, skill.Name, skill.Version))
	_, err = s.ReadSkill(t.Context(), p, skill.Name)
	code(t, err, "not_found")
	recreated, err := s.PutSkill(t.Context(), p, skill.Name, input)
	require.NoError(t, err)
	require.Greater(t, recreated.Version, skill.Version)
	input.Version = skill.Version
	_, err = s.PutSkill(t.Context(), p, skill.Name, input)
	code(t, err, "skill_version_conflict")
	input.Version = recreated.Version
	input.Body = "Updated instructions"
	updated, err := s.PutSkill(t.Context(), p, skill.Name, input)
	require.NoError(t, err)
	require.Equal(t, input.Body, updated.Body)
	input.Version = updated.Version
	input.Body = strings.Repeat("界", 16001)
	_, err = s.PutSkill(t.Context(), p, skill.Name, input)
	code(t, err, "invalid_skill")
	delete(p.Permissions, domain.SkillsWritePermission)
	err = s.DeleteSkill(t.Context(), p, skill.Name, updated.Version)
	code(t, err, "forbidden")
}

func TestCallerSkillsPersistAndBindIdempotency(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	p := principal("caller-skills")
	in := request()
	in.Skills = []domain.SkillDefinition{
		{
			Name:    "inspect-document",
			Usecase: "Inspect a document",
			Body:    "Cite evidence",
			Require: []string{"history.read"},
		},
	}
	run, err := s.CreateRun(t.Context(), p, "caller-skills", in)
	require.NoError(t, err)
	saved, err := s.ExecutionRun(t.Context(), run.ID)
	require.NoError(t, err)
	require.Equal(t, in.Skills, saved.Skills)
	in.Skills[0].Body = "Changed procedure"
	_, err = s.CreateRun(t.Context(), p, "caller-skills", in)
	code(t, err, "idempotency_conflict")
	in.Skills[0].Require = []string{"not-a-method"}
	_, err = s.CreateRun(t.Context(), p, "invalid-caller-skills", in)
	code(t, err, "invalid_skills")
}
