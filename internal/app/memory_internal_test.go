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

func TestPersonalMemorySandboxWritesReadsAndIsolates(t *testing.T) {
	t.Parallel()
	repository, _ := searchToolStore(t)
	p := domain.Principal{
		Issuer:      "issuer",
		Subject:     "alice",
		Actor:       "alice",
		OrgID:       "org",
		ExpiresAt:   time.Now().Add(time.Hour),
		Permissions: map[string]bool{"runs.create": true, "chat.read": true},
	}
	run, err := repository.CreateRun(
		t.Context(),
		p,
		"memory-script",
		domain.CreateRun{Input: domain.Input{Text: "remember"}},
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
	result, err := tools.executeScript(
		t.Context(),
		run,
		`await modules.memory.set_micro({text:'Prefers evening appointments',version:0}); await modules.memory.put({key:'preferences',kind:'note',title:'Preferences',summary:'Evenings',body:'User explicitly requested evenings.',version:0}); const page=await modules.memory.list({query:'evening'}); return {page,state:await modules.memory.state({}),note:await modules.memory.read({key:'preferences',limit:10})};`,
	)
	require.NoError(t, err)
	require.Contains(t, string(result), "Prefers evening appointments")
	require.Contains(t, string(result), `"partial":true`)
	require.Contains(t, string(result), "preferences")
	catalog, err := tools.tools(t.Context(), run, p, "")
	require.NoError(t, err)
	encoded, err := json.Marshal(catalog)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "lasting facts and preferences")
	page, err := tools.help(t.Context(), run, p, helpListMethod, json.RawMessage(`{"module":"memory"}`))
	require.NoError(t, err)
	require.Contains(t, string(page), `"method":"put"`)
	p.Subject = "other-user"
	_, err = tools.builtin(t.Context(), run, memoryModule, "state", json.RawMessage(`{}`))
	require.EqualError(t, err, "forbidden")
}
