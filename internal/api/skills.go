package api

import (
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"github.com/complynx/loom/internal/domain"
)

const skillPath = "/v1/skills/{name}"

func (s *server) registerSkills(mux *http.ServeMux) {
	s.route(mux, "GET /v1/skills", "chat.read", s.skillsList)
	s.route(mux, "GET /v1/skills/{name}", "chat.read", s.skillRead)
	s.route(mux, "PUT /v1/skills/{name}", domain.SkillsWritePermission, s.skillPut)
	s.route(mux, "DELETE /v1/skills/{name}", domain.SkillsWritePermission, s.skillDelete)
}

func (s *server) skillsList(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	in := domain.SkillListRequest{Query: r.URL.Query().Get("query"), AfterName: r.URL.Query().Get("after_name")}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil {
			return domain.NewError(http.StatusUnprocessableEntity, "invalid_skill")
		}
		in.Limit = limit
	}
	out, err := s.store.ListSkills(r.Context(), p, in)
	if err == nil {
		write(w, http.StatusOK, out)
	}
	return err
}

func (s *server) skillRead(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	out, err := s.store.ReadSkill(r.Context(), p, r.PathValue("name"))
	if err == nil {
		write(w, http.StatusOK, out)
	}
	return err
}

func (s *server) skillPut(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var in domain.SkillWrite
	if err := s.decode(w, r, &in); err != nil {
		return err
	}
	out, err := s.store.PutSkill(r.Context(), p, r.PathValue("name"), in)
	if err == nil {
		write(w, http.StatusOK, out)
	}
	return err
}

func (s *server) skillDelete(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var in memoryVersionRequest
	if err := s.decode(w, r, &in); err != nil {
		return err
	}
	if err := s.store.DeleteSkill(r.Context(), p, r.PathValue("name"), in.Version); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func skillParameters(path string) []*huma.Param {
	if path == skillPath {
		return []*huma.Param{
			{Name: "name", In: memoryPathLocation, Required: true, Schema: &huma.Schema{Type: schemaString}},
		}
	}
	if path != "/v1/skills" {
		return nil
	}
	params := []*huma.Param{}
	for _, name := range []string{"query", "after_name", memoryLimitQuery} {
		kind := schemaString
		if name == memoryLimitQuery {
			kind = schemaInteger
		}
		params = append(params, &huma.Param{Name: name, In: queryLocation, Schema: &huma.Schema{Type: kind}})
	}
	return params
}
func skillOperationTypes(method, path string) (any, any, string, bool) {
	if path == "/v1/skills" {
		return nil, domain.SkillListResult{}, "200", true
	}
	if path != skillPath {
		return nil, nil, "", false
	}
	switch method {
	case http.MethodPut:
		return domain.SkillWrite{}, domain.Skill{}, "200", true
	case http.MethodDelete:
		return memoryVersionRequest{}, nil, "204", true
	default:
		return nil, domain.Skill{}, "200", true
	}
}
