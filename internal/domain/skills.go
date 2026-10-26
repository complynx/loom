package domain

import (
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"
)

// SkillsWritePermission gates organization library changes independently of reading.
const SkillsWritePermission = "skills.write"

const (
	MaxSkillRequirements  = 50
	MaxSkillsPerSource    = 20
	MaxOrganizationSkills = 100
	MaxSkillName          = 64
	MaxSkillUsecase       = 1024
	MaxSkillBody          = 16000
)

type Skill struct {
	Require []string `json:"require,omitempty"`
	Source  string   `json:"source,omitempty"`
	Name    string   `json:"name"`
	Usecase string   `json:"usecase"`
	Body    string   `json:"body,omitempty"`
	Version int64    `json:"version"`
}

// SkillDefinition is supplied by a caller or an authorized method descriptor.
// It is scoped to that source and never edits the organization library.
type SkillDefinition struct {
	Require []string `json:"require,omitempty"`
	Name    string   `json:"name"`
	Usecase string   `json:"usecase"`
	Body    string   `json:"body"`
}

var skillName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
var skillDependency = regexp.MustCompile(`^([$]tools|[a-z][a-z0-9_.-]{0,127})\.[a-z][a-z0-9_.-]{0,127}$`)

func ValidSkillRequirements(require []string) bool {
	if len(require) > MaxSkillRequirements {
		return false
	}
	seen := map[string]bool{}
	for _, name := range require {
		if seen[name] || !skillDependency.MatchString(name) {
			return false
		}
		seen[name] = true
	}
	return true
}

func ValidSkill(name, usecase, body string) bool {
	validText := func(s string, limit int) bool {
		return utf8.ValidString(s) && !strings.ContainsRune(s, 0) && strings.TrimSpace(s) != "" &&
			utf8.RuneCountInString(s) <= limit
	}
	return len(name) <= MaxSkillName && skillName.MatchString(name) && validText(usecase, MaxSkillUsecase) &&
		validText(body, MaxSkillBody)
}

func ValidateSkills(items []SkillDefinition) error {
	if len(items) > MaxSkillsPerSource {
		return NewError(http.StatusUnprocessableEntity, "invalid_skills")
	}
	seen := map[string]bool{}
	for _, item := range items {
		if seen[item.Name] || !ValidSkill(item.Name, item.Usecase, item.Body) || !ValidSkillRequirements(item.Require) {
			return NewError(http.StatusUnprocessableEntity, "invalid_skills")
		}
		seen[item.Name] = true
	}
	return nil
}

type SkillWrite struct {
	Require []string `json:"require,omitempty"`
	Usecase string   `json:"usecase"`
	Body    string   `json:"body"`
	Version int64    `json:"version"`
}

type SkillListRequest struct {
	Query     string `json:"query,omitempty"`
	AfterName string `json:"after_name,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type SkillListResult struct {
	Skills        []Skill `json:"skills"`
	Partial       bool    `json:"partial"`
	NextAfterName string  `json:"next_after_name,omitempty"`
}
