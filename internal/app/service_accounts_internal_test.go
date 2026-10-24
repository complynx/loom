package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/modules"
	"github.com/complynx/loom/internal/operations"
	"github.com/complynx/loom/internal/provider"
	"github.com/complynx/loom/internal/sandbox"
)

func TestServiceAccountCanReadAndWriteAuthorizedService(t *testing.T) {
	t.Parallel()
	repository, _ := searchToolStore(t)
	p := descriptorPrincipal()
	var reads, writes, denied atomic.Int32
	manifest := accountServiceManifest()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/manifest":
			_ = json.NewEncoder(w).Encode(manifest)
		case "/read", "/write":
			assert.Equal(t, "Bearer descriptor-token", r.Header.Get("Authorization"))
			var input struct {
				OperationID string `json:"operation_id"`
			}
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&input)) {
				http.Error(w, "invalid envelope", http.StatusBadRequest)
				return
			}
			if r.URL.Path == "/read" {
				reads.Add(1)
			} else {
				writes.Add(1)
			}
			_ = json.NewEncoder(w).
				Encode(map[string]any{"operation_id": input.OperationID, "status": "succeeded", "result": map[string]bool{"ok": true}})
		default:
			denied.Add(1)
			http.Error(w, "unrelated business unavailable", http.StatusForbidden)
		}
	}))
	t.Cleanup(server.Close)
	address, err := url.Parse(server.URL)
	require.NoError(t, err)
	registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		return []modules.Endpoint{
			{ContainerID: "service", URI: "http://registered:" + address.Port() + "/manifest", Address: "127.0.0.1"},
		}, nil
	}), modules.Options{})
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(t.Context()))
	run, err := repository.CreateRun(
		t.Context(),
		p,
		"service-run",
		domain.CreateRun{Input: domain.Input{Text: "use authorized service"}},
	)
	require.NoError(t, err)
	run, err = repository.ClaimRun(t.Context(), run.ID, "worker", time.Minute)
	require.NoError(t, err)
	authorize := func(context.Context, domain.Run) (domain.Principal, string, error) { return p, "descriptor-token", nil }
	tools := runtimeTools{repository: repository, registry: registry, authorize: authorize, executor: sandbox.Inline{},
		cfg: config.Config{Sandbox: config.Sandbox{ScriptTimeoutCeilingMS: 5000}}}
	tools.bridge = NewModuleBridge(registry, &operations.Coordinator{Pool: repository.Pool()}, repository, authorize)
	catalog, err := tools.tools(t.Context(), run, p, "")
	require.NoError(t, err)
	var hasWrite bool
	for _, tool := range catalog {
		if tool.Name == "module_call" {
			hasWrite = true
		}
	}
	require.True(t, hasWrite)
	result, err := tools.executeScript(t.Context(), run, `return await modules.business.read({})`)
	require.NoError(t, err)
	require.JSONEq(t, `{"ok":true}`, string(result))
	call := provider.Call{
		ID:        "write",
		Name:      "module_call",
		Arguments: json.RawMessage(`{"module":"business","method":"write","arguments":{},"confirmation_text":""}`),
	}
	result, approval, err := tools.bridge.Call(t.Context(), run, p, "", call)
	require.NoError(t, err)
	require.Nil(t, approval)
	require.JSONEq(t, `{"ok":true}`, string(result))
	_, _, err = tools.bridge.Call(t.Context(), run, p, "", call)
	require.NoError(t, err)
	require.EqualValues(t, 1, writes.Load(), "same durable call must not write twice")
	require.EqualValues(t, 1, reads.Load())
	require.Zero(t, denied.Load())
	_, err = tools.executeScript(t.Context(), run, `return await modules.memory.set_micro({text:"escape",version:0})`)
	require.Error(t, err)
	_, err = repository.SetMemoryMicro(t.Context(), p, domain.MemoryWrite{Text: "escape"})
	require.Error(t, err)
	delete(p.Permissions, "business.access")
	_, err = tools.executeScript(t.Context(), run, `return await modules.business.read({})`)
	require.Error(t, err)
	require.EqualValues(t, 1, reads.Load(), "current permission removal must block dispatch")
	require.Zero(t, denied.Load())
}

func accountServiceManifest() modules.Manifest {
	methods := []modules.Method{}
	for _, effect := range []string{"read", "write"} {
		method := modules.Method{
			ID:             effect,
			Description:    "Authorized service method",
			Permissions:    []string{"business.access"},
			Path:           "/" + effect,
			Effect:         effect,
			TimeoutMS:      3000,
			MaxResultBytes: 4096,
			InputSchema:    json.RawMessage(`{"type":"object","additionalProperties":false}`),
			OutputSchema: json.RawMessage(
				`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`,
			),
			Confirmation:      modules.Confirmation{Mode: "none"},
			ArtifactMIMETypes: []string{},
		}
		if effect == "write" {
			method.Idempotency = true
			method.StatusPath = "/status"
			method.DeduplicationWindowSeconds = 86400
		}
		methods = append(methods, method)
	}
	return modules.Manifest{
		ProtocolVersion: 1,
		ServiceID:       "account-service",
		Revision:        "v1",
		Description:     "Service account fixture",
		Modules: []modules.Module{
			{
				ID:          "business",
				Description: "Authorized methods",
				Permissions: []string{"business.access"},
				Discovery:   "static",
				Methods:     methods,
			},
			{
				ID:              "unrelated",
				Description:     "Denied dynamic catalog",
				Permissions:     []string{"unrelated.access"},
				Discovery:       "dynamic",
				DiscoveryPath:   "/unrelated",
				CacheTTLSeconds: 1,
			},
		},
	}
}
