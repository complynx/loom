package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/modules"
	"github.com/complynx/loom/internal/sandbox"
	"github.com/complynx/loom/internal/telemetry"
)

func TestDiagnosticsCatalogRequiresCurrentPermission(t *testing.T) {
	t.Parallel()
	p := domain.Principal{Subject: "operator", OrgID: "org", Permissions: map[string]bool{"chat.read": true}}
	tools := runtimeTools{
		registry:  &modules.Registry{},
		authorize: func(context.Context, domain.Run) (domain.Principal, string, error) { return p, "", nil },
	}
	catalog, err := tools.tools(t.Context(), domain.Run{}, p, "")
	require.NoError(t, err)
	encoded, err := json.Marshal(catalog)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "diagnose-run")
	p.Permissions[diagnosticsPermission] = true
	catalog, err = tools.tools(t.Context(), domain.Run{}, p, "")
	require.NoError(t, err)
	encoded, err = json.Marshal(catalog)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "diagnose-run")
	delete(p.Permissions, diagnosticsPermission)
	_, err = tools.diagnostics(t.Context(), p, "overview", json.RawMessage(`{}`))
	require.EqualError(t, err, "forbidden")
}

func TestDiagnosticsFunctionsExecuteInSandboxWithCurrentRights(t *testing.T) {
	t.Parallel()
	repository, _ := searchToolStore(t)
	p := domain.Principal{
		Issuer: "issuer", Subject: "operator", Actor: "operator", OrgID: "org",
		ExpiresAt:   time.Now().Add(time.Hour),
		Permissions: map[string]bool{"runs.create": true, "chat.read": true, diagnosticsPermission: true},
	}
	run, err := repository.CreateRun(
		t.Context(), p, "diagnostics-script", domain.CreateRun{Input: domain.Input{Text: "inspect"}},
	)
	require.NoError(t, err)
	run, err = repository.ClaimRun(t.Context(), run.ID, "worker", time.Minute)
	require.NoError(t, err)
	authorize := func(context.Context, domain.Run) (domain.Principal, string, error) { return p, "", nil }
	tools := runtimeTools{
		repository: repository, registry: &modules.Registry{}, executor: sandbox.Inline{}, authorize: authorize,
		observer: telemetry.New(slog.New(slog.DiscardHandler)),
		cfg:      config.Config{Sandbox: config.Sandbox{ScriptTimeoutCeilingMS: 5000}},
	}
	tools.bridge = NewModuleBridge(tools.registry, nil, repository, authorize)
	result, err := tools.executeScript(
		t.Context(),
		run,
		"const page = await modules.diagnostics.runs({}); const detail = await modules.diagnostics.run({run_id: page.items[0].id}); return {id:detail.run.id, health:await modules.diagnostics.health({})};",
	)
	require.NoError(t, err)
	require.Contains(t, string(result), run.ID)
	require.Contains(t, string(result), "dependencies")
	delete(p.Permissions, diagnosticsPermission)
	_, err = tools.builtin(t.Context(), run, diagnosticsModule, "overview", json.RawMessage(`{}`))
	require.EqualError(t, err, "forbidden")
}
