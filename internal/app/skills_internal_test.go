package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/modules"
	"github.com/complynx/loom/internal/sandbox"
)

func TestSkillSourcesRequirementsAndImmediateMetadata(t *testing.T) {
	t.Parallel()
	p := domain.Principal{
		Subject:        "alice",
		OrgID:          "org",
		ServiceAccount: true,
		Permissions:    map[string]bool{"chat.read": true},
	}
	tools := runtimeTools{
		registry:  &modules.Registry{},
		authorize: func(context.Context, domain.Run) (domain.Principal, string, error) { return p, "", nil },
	}
	run := domain.Run{Skills: []domain.SkillDefinition{
		{
			Name:    "same-name",
			Usecase: "Caller document procedure",
			Body:    "PRIVATE FULL BODY",
			Require: []string{"history.read"},
		},
		{Name: "unavailable", Usecase: "HIDDEN USECASE", Body: "Hidden", Require: []string{"private.read"}},
	}}
	reads := []modules.Tool{
		{
			Module: "instrument",
			Method: "list",
			Skills: []domain.SkillDefinition{
				{
					Name:    "same-name",
					Usecase: "Tool procedure",
					Body:    "TOOL FULL BODY",
					Require: []string{"instrument.list"},
				},
			},
		},
	}
	catalog, err := tools.skillCatalog(t.Context(), run, p, reads, nil)
	require.NoError(t, err)
	require.Len(t, catalog, 2)
	require.Equal(t, "caller", catalog[0].Source)
	require.Equal(t, "tool:instrument/list", catalog[1].Source)
	entries, _, err := tools.helpEntries(run, p, reads, nil)
	require.NoError(t, err)
	selected, err := tools.readCatalogSkill(t.Context(), p, catalog, entries, "same-name", "tool:instrument/list")
	require.NoError(t, err)
	require.Equal(t, "TOOL FULL BODY", selected.Body)
	definitions, err := tools.tools(t.Context(), run, p, "")
	require.NoError(t, err)
	raw, err := json.Marshal(definitions)
	require.NoError(t, err)
	require.Contains(t, string(raw), "Caller document procedure")
	require.NotContains(t, string(raw), "PRIVATE FULL BODY")
	require.NotContains(t, string(raw), "HIDDEN USECASE")
	page, err := skillPage(catalog, domain.SkillListRequest{Limit: 1})
	require.NoError(t, err)
	require.True(t, page.Partial)
	tail, err := skillPage(catalog, domain.SkillListRequest{Limit: 1, AfterName: page.NextAfterName})
	require.NoError(t, err)
	require.Equal(t, "tool:instrument/list", tail.Skills[0].Source)
	_, err = tools.readCatalogSkill(t.Context(), p, catalog, entries, "same-name", "")
	require.EqualError(t, err, "not_found")
	run.ExecutionProfile = domain.ProfileNativeFiles
	catalog, err = tools.skillCatalog(t.Context(), run, p, reads, nil)
	require.NoError(t, err)
	require.Empty(t, catalog)
}

func TestSkillsScriptWritesRecheckPermission(t *testing.T) {
	t.Parallel()
	repository, _ := searchToolStore(t)
	p := domain.Principal{
		Issuer:      "issuer",
		Subject:     "alice",
		Actor:       "alice",
		OrgID:       "org",
		ExpiresAt:   time.Now().Add(time.Hour),
		Permissions: map[string]bool{"runs.create": true, "chat.read": true, "skills.write": true},
	}
	run, err := repository.CreateRun(
		t.Context(),
		p,
		"skill-script",
		domain.CreateRun{Input: domain.Input{Text: "Manage skills"}},
	)
	require.NoError(t, err)
	run, err = repository.ClaimRun(t.Context(), run.ID, "worker", time.Minute)
	require.NoError(t, err)
	authorize := func(context.Context, domain.Run) (domain.Principal, string, error) { return p, "", nil }
	tools := runtimeTools{
		repository: repository,
		registry:   &modules.Registry{},
		executor:   sandbox.Inline{},
		authorize:  authorize,
		cfg:        config.Config{Sandbox: config.Sandbox{ScriptTimeoutCeilingMS: 5000}},
	}
	tools.bridge = NewModuleBridge(tools.registry, nil, repository, authorize)
	out, err := tools.executeScript(
		t.Context(),
		run,
		`await modules.skills.put({name:"inspect-document",usecase:"Inspect a document",body:"Read relevant pages",require:["history.read"],version:0}); return await modules.skills.read({name:"inspect-document"});`,
	)
	require.NoError(t, err)
	require.Contains(t, string(out), "Read relevant pages")
	delete(p.Permissions, "skills.write")
	_, err = tools.builtin(
		t.Context(),
		run,
		skillsModule,
		"put",
		json.RawMessage(`{"name":"inspect-document","usecase":"Changed","body":"Changed","version":1}`),
	)
	require.EqualError(t, err, "forbidden")
	page, err := tools.help(t.Context(), run, p, "list", json.RawMessage(`{"module":"skills"}`))
	require.NoError(t, err)
	require.NotContains(t, string(page), `"method":"put"`)
	require.Contains(t, string(page), `"method":"read"`)
	p.OrgID = "another-org"
	_, err = tools.builtin(t.Context(), run, skillsModule, "read", json.RawMessage(`{"name":"inspect-document"}`))
	require.EqualError(t, err, "forbidden")
}

func TestToolSkillSourcesSeparateDottedIdentifiers(t *testing.T) {
	t.Parallel()
	p := domain.Principal{
		Subject:        "reader",
		OrgID:          "org",
		ServiceAccount: true,
		Permissions:    map[string]bool{"chat.read": true},
	}
	tools := runtimeTools{}
	reads := []modules.Tool{
		{
			Module: "a.b",
			Method: "c",
			Skills: []domain.SkillDefinition{{Name: "same-name", Usecase: "First", Body: "First body"}},
		},
		{
			Module: "a",
			Method: "b.c",
			Skills: []domain.SkillDefinition{{Name: "same-name", Usecase: "Second", Body: "Second body"}},
		},
	}
	catalog, err := tools.skillCatalog(t.Context(), domain.Run{}, p, reads, nil)
	require.NoError(t, err)
	require.Len(t, catalog, 2)
	entries, _, err := tools.helpEntries(domain.Run{}, p, reads, nil)
	require.NoError(t, err)
	for source, body := range map[string]string{"tool:a.b/c": "First body", "tool:a/b.c": "Second body"} {
		skill, readErr := tools.readCatalogSkill(t.Context(), p, catalog, entries, "same-name", source)
		require.NoError(t, readErr)
		require.Equal(t, body, skill.Body)
	}
	first, err := skillPage(catalog, domain.SkillListRequest{Limit: 1})
	require.NoError(t, err)
	second, err := skillPage(catalog, domain.SkillListRequest{Limit: 1, AfterName: first.NextAfterName})
	require.NoError(t, err)
	require.Len(t, second.Skills, 1)
	require.NotEqual(t, first.Skills[0].Source, second.Skills[0].Source)
}
