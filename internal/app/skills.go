package app

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/identity"
	"github.com/complynx/loom/internal/sandbox"
)

const skillsModule = "skills"
const builtinDeleteMethod = "delete"
const builtinPutMethod = "put"
const invalidArgumentsCode = "invalid_arguments"

func skillMethods(p domain.Principal) []string {
	if !p.Has("chat.read") {
		return nil
	}
	methods := []string{helpListMethod, builtinReadMethod}
	if p.Has(domain.SkillsWritePermission) {
		methods = append(methods, builtinPutMethod, builtinDeleteMethod)
	}
	return methods
}

func (t runtimeTools) skills(
	ctx context.Context,
	run domain.Run,
	method string,
	raw json.RawMessage,
) (json.RawMessage, error) {
	if run.NativeFilesOnly() {
		return nil, domain.Forbidden()
	}
	if method == builtinPutMethod || method == builtinDeleteMethod {
		ctx = identity.RequireFresh(ctx)
	}
	p, token, err := t.fresh(ctx, run)
	if err != nil {
		return nil, err
	}
	if p.OrgID != run.OrgID || !slices.Contains(skillMethods(p), method) {
		return nil, domain.Forbidden()
	}
	if method == helpListMethod || method == builtinReadMethod {
		return t.readSkills(ctx, run, p, token, method, raw)
	}
	var result any
	switch method {
	case builtinPutMethod:
		var in struct {
			domain.SkillWrite

			Name string `json:"name"`
		}
		if err = decodeDiagnosticsInput(raw, &in); err == nil {
			result, err = t.repository.PutSkill(ctx, p, in.Name, in.SkillWrite)
		}
	case builtinDeleteMethod:
		var in struct {
			Name    string `json:"name"`
			Version int64  `json:"version"`
		}
		if err = decodeDiagnosticsInput(raw, &in); err == nil {
			err = t.repository.DeleteSkill(ctx, p, in.Name, in.Version)
		}
	default:
		return nil, domain.NotFound()
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

func appendSkillBindings(bindings []sandbox.Binding, p domain.Principal) []sandbox.Binding {
	if methods := skillMethods(p); len(methods) > 0 {
		return append(bindings, sandbox.Binding{Module: skillsModule, Methods: methods})
	}
	return bindings
}

func (t runtimeTools) readSkills(
	ctx context.Context,
	run domain.Run,
	p domain.Principal,
	token, method string,
	raw json.RawMessage,
) (json.RawMessage, error) {
	reads, err := t.runReadCatalog(ctx, run, p, token)
	if err != nil {
		return nil, err
	}
	actions, err := t.runActionCatalog(ctx, run, p, token)
	if err != nil {
		return nil, err
	}
	catalog, err := t.skillCatalog(ctx, run, p, reads, actions)
	if err != nil {
		return nil, err
	}
	var result any
	if method == helpListMethod {
		var in domain.SkillListRequest
		if err = decodeDiagnosticsInput(raw, &in); err != nil {
			return nil, err
		}
		result, err = skillPage(catalog, in)
	} else {
		var in struct {
			Name   string `json:"name"`
			Source string `json:"source,omitempty"`
		}
		if err = decodeDiagnosticsInput(raw, &in); err != nil {
			return nil, err
		}
		entries, _, helpErr := t.helpEntries(run, p, reads, actions)
		if helpErr != nil {
			return nil, helpErr
		}
		result, err = t.readCatalogSkill(ctx, p, catalog, entries, in.Name, in.Source)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}
