package app

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/artifacts"
	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/fileproc"
	"github.com/complynx/loom/internal/modules"
	"github.com/complynx/loom/internal/provider"
	"github.com/complynx/loom/internal/runs"
	"github.com/complynx/loom/internal/sandbox"
)

func unavailableBusinessRegistry(t *testing.T) (*modules.Registry, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manifest" {
			_, _ = io.WriteString(
				w,
				`{"protocol_version":1,"service_id":"business","revision":"v1","description":"Unavailable business service","modules":[{"id":"business","description":"Business methods","permissions":["business.access"],"discovery":"dynamic","discovery_path":"/discover","cache_ttl_seconds":0}]}`,
			)
			return
		}
		requests.Add(1)
		http.Error(w, "No business account", http.StatusForbidden)
	}))
	t.Cleanup(server.Close)
	address, err := url.Parse(server.URL)
	require.NoError(t, err)
	registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		return []modules.Endpoint{
			{ContainerID: "business", URI: "http://registered:" + address.Port() + "/manifest", Address: "127.0.0.1"},
		}, nil
	}), modules.Options{})
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(t.Context()))
	return registry, &requests
}

func descriptorPrincipal() domain.Principal {
	return domain.Principal{
		ServiceAccount: true,
		Issuer:         "issuer",
		Subject:        "descriptor",
		Actor:          "descriptor",
		OrgID:          "org",
		ExpiresAt:      time.Now().Add(time.Hour),
		Permissions: map[string]bool{
			"chat.read":       true,
			"runs.create":     true,
			"artifacts.read":  true,
			"business.access": true,
		},
	}
}

func TestNativeFilesSkipsDeniedDiscoveryAndRejectsEscape(t *testing.T) {
	t.Parallel()
	registry, requests := unavailableBusinessRegistry(t)
	p := descriptorPrincipal()
	tools := runtimeTools{
		registry: registry,
		executor: sandbox.Inline{},
		cfg: config.Config{
			FileTools: config.FileTools{Enabled: true},
			Sandbox:   config.Sandbox{ScriptTimeoutCeilingMS: 5000},
		},
		authorize: func(context.Context, domain.Run) (domain.Principal, string, error) { return p, "descriptor-token", nil },
	}
	_, err := tools.tools(t.Context(), domain.Run{}, p, "")
	require.Error(t, err)
	require.Positive(t, requests.Swap(0))
	run := domain.Run{ExecutionProfile: domain.ProfileNativeFiles}
	catalog, err := tools.tools(t.Context(), run, p, "")
	require.NoError(t, err)
	require.NotEmpty(t, catalog)
	entries, _, _, err := tools.helpCatalog(t.Context(), run, p, "descriptor-token")
	require.NoError(t, err)
	for _, entry := range entries {
		require.True(t, nativeFileMethod(entry.Module, entry.Method), entry.ID)
	}
	for _, code := range []string{
		`return await modules.business.write({})`,
		`return await modules.memory.set_micro({text:"escape",version:0})`,
		`return await modules.artifacts.fetch({module:"business",method:"read",external_id:"x"})`,
	} {
		_, err = tools.executeScript(t.Context(), run, code)
		require.Error(t, err)
	}
	_, _, err = tools.bridge.Call(t.Context(), run, p, "", provider.Call{Name: "module_call"})
	require.Error(t, err)
	err = tools.authorizeFileOrigin(t.Context(), run, domain.Artifact{})
	require.Error(t, err)
	_, err = tools.fetchExternal(t.Context(), run, nil, nil)
	require.Error(t, err)
	require.Zero(t, requests.Load())
}

func TestNativeFilesTGSRunWithUnavailableBusiness(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("file worker requires Linux")
	}
	if _, err := exec.LookPath("loom-animation"); err != nil {
		t.Skip("run in the file-runtime-tests image")
	}
	repository, _ := searchToolStore(t)
	registry, requests := unavailableBusinessRegistry(t)
	p := descriptorPrincipal()
	uid, err := repository.ResolveUser(t.Context(), p)
	require.NoError(t, err)
	legacy := p
	legacy.ServiceAccount = false
	_, err = repository.SetMemoryMicro(t.Context(), legacy, domain.MemoryWrite{Text: "PRIVATE_MEMORY_MARKER"})
	require.NoError(t, err)
	files, err := artifacts.New(t.TempDir(), repository, artifacts.Limits{MaxUploadBytes: 2 << 20, QuotaBytes: 8 << 20})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, files.Close()) })
	var compressed bytes.Buffer
	w := gzip.NewWriter(&compressed)
	_, err = io.WriteString(
		w,
		`{"v":"5.7.4","fr":1,"ip":0,"op":2,"w":64,"h":64,"ddd":0,"assets":[],"layers":[{"ddd":0,"ind":1,"ty":4,"sr":1,"ks":{"o":{"a":0,"k":100},"r":{"a":0,"k":0},"p":{"a":0,"k":[0,0,0]},"a":{"a":0,"k":[0,0,0]},"s":{"a":0,"k":[100,100,100]}},"shapes":[{"ty":"rc","d":1,"s":{"a":0,"k":[64,64]},"p":{"a":0,"k":[32,32]},"r":{"a":0,"k":0}},{"ty":"fl","c":{"a":1,"k":[{"t":0,"s":[1,0,0,1],"h":1},{"t":1,"s":[0,0,1,1]}]},"o":{"a":0,"k":100},"r":1}],"ip":0,"op":2,"st":0,"bm":0}]}`,
	)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	artifact, err := files.Upload(
		t.Context(),
		artifacts.Principal{OwnerID: uid, OrgID: p.OrgID},
		"application/x-tgsticker",
		&compressed,
	)
	require.NoError(t, err)
	chat, err := repository.CreateChat(t.Context(), p)
	require.NoError(t, err)
	run, err := repository.CreateRun(t.Context(), p, "native-tgs", domain.CreateRun{
		ExecutionProfile: domain.ProfileNativeFiles, ChatID: chat.ID,
		Input: domain.Input{Text: "Inspect original TGS artifact " + artifact.ID},
	})
	require.NoError(t, err)
	socket := filepath.Join(t.TempDir(), "files.sock")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	server := &http.Server{Handler: &fileproc.Handler{}, ReadHeaderTimeout: time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() { require.NoError(t, server.Close()); require.ErrorIs(t, <-done, http.ErrServerClosed) })
	authorize := func(context.Context, domain.Run) (domain.Principal, string, error) { return p, "descriptor-token", nil }
	tools := runtimeTools{
		repository: repository,
		files:      files,
		registry:   registry,
		executor:   sandbox.Inline{},
		authorize:  authorize,
		cfg: config.Config{Providers: map[string]config.Provider{"openai": {}},
			Artifacts: config.Artifacts{MaxUploadBytes: 2 << 20},
			FileTools: config.FileTools{Enabled: true, Socket: socket, TimeoutMS: 10000},
			Sandbox:   config.Sandbox{ScriptTimeoutCeilingMS: 20000}},
	}
	tools.bridge = NewModuleBridge(registry, nil, repository, authorize)
	code := `const help=await modules.help.read({module:"files",method:"video_storyboard"}); const h=await modules.artifacts.open({artifact_id:"` + artifact.ID + `"}); return await modules.files.video_storyboard({handle_id:h.handle_id,max_frames:2,interval:1,width:64});`
	args, err := json.Marshal(map[string]string{"code": code})
	require.NoError(t, err)
	model := &nativeFilesModel{}
	model.call = provider.Call{ID: "frames", Name: "script", Arguments: args}
	engine := runs.New(
		repository,
		model,
		runs.Options{Authorize: authorize, Tools: tools.tools, Script: tools.script, Attachment: tools.attachArtifact},
	)
	require.NoError(t, engine.Execute(t.Context(), run.ID))
	require.Empty(t, model.failure)
	completed, err := repository.GetRun(t.Context(), p, run.ID)
	require.NoError(t, err)
	require.Equal(t, domain.Completed, completed.State)
	require.Len(t, model.requests, 3)
	encoded, err := json.Marshal(model.requests[2].Input)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "input_image")
	require.Positive(t, model.counts)
	for _, request := range model.requests {
		require.NotContains(t, request.Instructions, "PRIVATE_MEMORY_MARKER")
	}
	require.Zero(t, requests.Load())
}

type nativeFilesModel struct {
	transcriptModel

	counts  int
	failure string
}

func (m *nativeFilesModel) Respond(ctx context.Context, request provider.Request) (provider.Response, error) {
	if len(m.requests) == 2 {
		m.requests = append(m.requests, request)
		text := `{"description":"synthetic red-to-blue animation"}`
		return provider.Response{
			ID:     "final",
			Text:   text,
			Output: []provider.Item{provider.Message("assistant", text)},
		}, nil
	}
	if len(m.requests) != 1 {
		return m.transcriptModel.Respond(ctx, request)
	}
	for _, item := range request.Input {
		var output struct {
			CallID string `json:"call_id"`
			Output string `json:"output"`
		}
		if json.Unmarshal(item, &output) != nil || output.CallID != "frames" || output.Output == "" {
			continue
		}
		var result fileResult
		if err := json.Unmarshal([]byte(output.Output), &result); err != nil {
			return provider.Response{}, err
		}
		if len(result.Outputs) != 2 {
			m.failure = output.Output
			return provider.Response{}, fmt.Errorf("expected two TGS frames: %s", output.Output)
		}
		m.requests = append(m.requests, request)
		args, err := json.Marshal(map[string]string{"handle_id": result.Outputs[0].HandleID})
		if err != nil {
			return provider.Response{}, err
		}
		call := provider.Call{ID: "attach", Name: "artifact_attach", Arguments: args}
		raw, err := json.Marshal(
			map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Name, "arguments": string(args)},
		)
		return provider.Response{ID: "attach-response", Calls: []provider.Call{call}, Output: []provider.Item{raw}}, err
	}
	return provider.Response{}, fmt.Errorf("missing TGS frame result")
}

func (m *nativeFilesModel) CountInputTokens(context.Context, provider.Request) (int64, error) {
	m.counts++
	return 100, nil
}
