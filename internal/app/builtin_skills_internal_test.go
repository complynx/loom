package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/modules"
)

func TestBuiltinSkillsGuideHiddenMethodsWithoutGrantingRights(t *testing.T) {
	t.Parallel()
	p := domain.Principal{
		Subject: "editor",
		OrgID:   "org",
		Permissions: map[string]bool{
			"chat.read":           true,
			"skills.write":        true,
			"artifacts.read":      true,
			diagnosticsPermission: true,
		},
	}
	tools := runtimeTools{
		cfg:       config.Config{FileTools: config.FileTools{Enabled: true}},
		registry:  &modules.Registry{},
		authorize: func(context.Context, domain.Run) (domain.Principal, string, error) { return p, "", nil },
	}
	run := domain.Run{OrgID: "org"}
	catalog, err := tools.skillCatalog(t.Context(), run, p, nil, nil)
	require.NoError(t, err)
	require.Len(t, catalog, 5)
	var definitions []domain.SkillDefinition
	require.NoError(t, json.Unmarshal(builtinSkillsJSON, &definitions))
	require.NoError(t, domain.ValidateSkills(definitions))
	entries, _, err := tools.helpEntries(run, p, nil, nil)
	require.NoError(t, err)
	manager, err := tools.readCatalogSkill(t.Context(), p, catalog, entries, "manage-skills", builtinSkillSource)
	require.NoError(t, err)
	require.EqualValues(t, 1, manager.Version)
	require.Contains(t, manager.Body, "modules.skills.put")
	page, err := tools.help(t.Context(), run, p, helpListMethod, json.RawMessage(`{"module":"skills","query":"put"}`))
	require.NoError(t, err)
	require.NotContains(t, string(page), `"method":"put"`)
	card, err := tools.help(
		t.Context(),
		run,
		p,
		builtinReadMethod,
		json.RawMessage(`{"module":"skills","method":"put"}`),
	)
	require.NoError(t, err)
	require.Contains(t, string(card), "skills.write")
	overview, err := environmentOverview(entries, &domain.DiscoveryOptions{ExpandServices: []string{"$loom"}})
	require.NoError(t, err)
	require.NotContains(t, string(overview), "diagnostics")
	require.NotContains(t, string(overview), "set_micro")
	files, err := tools.help(t.Context(), run, p, helpListMethod, json.RawMessage(`{"module":"files"}`))
	require.NoError(t, err)
	require.Contains(t, string(files), "pdf_text")
	delete(p.Permissions, "skills.write")
	catalog, err = tools.skillCatalog(t.Context(), run, p, nil, nil)
	require.NoError(t, err)
	for _, skill := range catalog {
		require.NotEqual(t, "manage-skills", skill.Name)
	}
	_, err = tools.skills(t.Context(), run, "put", json.RawMessage(`{}`))
	require.EqualError(t, err, "forbidden")
	p.ServiceAccount = true
	catalog, err = tools.skillCatalog(t.Context(), run, p, nil, nil)
	require.NoError(t, err)
	for _, skill := range catalog {
		require.NotEqual(t, "manage-memory", skill.Name)
	}
}

func TestBuiltinSkillReadSurvivesRemoteDiscoveryFailure(t *testing.T) {
	t.Parallel()
	manifest := modules.Manifest{
		ProtocolVersion: 1,
		ServiceID:       "offline",
		Revision:        "1",
		Description:     "Unavailable service",
		Modules: []modules.Module{
			{
				ID:              "offline",
				Description:     "Unavailable dynamic methods",
				Permissions:     []string{},
				Discovery:       "dynamic",
				DiscoveryPath:   "/discover",
				CacheTTLSeconds: 1,
			},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manifest" {
			_ = json.NewEncoder(w).Encode(manifest)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	address, err := url.Parse(server.URL)
	require.NoError(t, err)
	registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		return []modules.Endpoint{
			{ContainerID: "offline", URI: "http://offline:" + address.Port() + "/manifest", Address: "127.0.0.1"},
		}, nil
	}), modules.Options{})
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(t.Context()))
	p := domain.Principal{
		Subject:     "operator",
		OrgID:       "org",
		Permissions: map[string]bool{"chat.read": true, diagnosticsPermission: true},
	}
	tools := runtimeTools{
		registry:  registry,
		authorize: func(context.Context, domain.Run) (domain.Principal, string, error) { return p, "test-token", nil },
	}
	run := domain.Run{
		OrgID: "org",
		Skills: []domain.SkillDefinition{
			{
				Name:    "offline-procedure",
				Usecase: "Requires offline data",
				Body:    "Read offline data",
				Require: []string{"offline.read"},
			},
		},
	}
	modelTools, err := tools.tools(t.Context(), run, p, "")
	require.NoError(t, err)
	raw, err := json.Marshal(modelTools)
	require.NoError(t, err)
	require.Contains(t, string(raw), "diagnose-run")
	require.NotContains(t, string(raw), "offline-procedure")
	result, err := tools.skills(
		t.Context(),
		run,
		builtinReadMethod,
		json.RawMessage(`{"name":"diagnose-run","source":"builtin"}`),
	)
	require.NoError(t, err)
	require.Contains(t, string(result), "read-only")
	page, err := tools.skills(t.Context(), run, helpListMethod, json.RawMessage(`{}`))
	require.NoError(t, err)
	require.Contains(t, string(page), "diagnose-run")
	require.NotContains(t, string(page), "offline-procedure")
	delete(p.Permissions, diagnosticsPermission)
	_, err = tools.skills(
		t.Context(),
		run,
		builtinReadMethod,
		json.RawMessage(`{"name":"diagnose-run","source":"builtin"}`),
	)
	require.Error(t, err)
}
