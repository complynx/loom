package modules_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/modules"
)

func example(t *testing.T) modules.Manifest {
	t.Helper()
	raw, err := os.ReadFile("../../examples/manifest.json")
	require.NoError(t, err)
	var manifest modules.Manifest
	require.NoError(t, json.Unmarshal(raw, &manifest))
	return manifest
}
func endpoint(t *testing.T, address, id string) modules.Endpoint {
	t.Helper()
	u, err := url.Parse(address)
	require.NoError(t, err)
	return modules.Endpoint{
		ContainerID: id,
		URI:         "http://registered-service:" + u.Port() + "/manifest",
		Address:     "127.0.0.1",
	}
}
func principal() modules.Principal {
	return modules.Principal{
		Issuer:      "https://id.example",
		Subject:     "user",
		Token:       "delegated-token",
		Permissions: []string{"zns.massage.access", "zns.massage.read", "zns.massage.book", "zns.orders.access"},
	}
}
func TestReadBrokerFiltersAndValidates(t *testing.T) {
	t.Parallel()
	manifest := example(t)
	manifest.Modules = manifest.Modules[:1]
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manifest" {
			_ = json.NewEncoder(w).Encode(manifest)
			return
		}
		assert.Equal(t, "Bearer delegated-token", r.Header.Get("Authorization"))
		var request struct {
			OperationID string         `json:"operation_id"`
			Arguments   map[string]any `json:"arguments"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			return
		}
		assert.Equal(t, "andrey", request.Arguments["specialist_id"])
		calls.Add(1)
		_ = json.NewEncoder(w).
			Encode(map[string]any{"status": "succeeded", "operation_id": request.OperationID, "result": map[string]any{"slots": []any{}}})
	}))
	defer server.Close()
	source := modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		return []modules.Endpoint{endpoint(t, server.URL, "one")}, nil
	})
	registry, err := modules.NewRegistry(source, modules.Options{})
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(context.Background()))
	tools, err := registry.List(context.Background(), principal())
	require.NoError(t, err)
	require.Len(t, tools, 1)
	require.Equal(t, "list_slots", tools[0].Method)
	var guards atomic.Int32
	broker := registry.Broker(principal(), "run-one", func(context.Context) error { guards.Add(1); return nil })
	result, err := broker.Call(
		context.Background(),
		"massage",
		"list_slots",
		json.RawMessage(`{"specialist_id":"andrey"}`),
	)
	require.NoError(t, err)
	require.JSONEq(t, `{"slots":[]}`, string(result))
	require.EqualValues(t, 2, guards.Load())
	_, err = broker.Call(context.Background(), "massage", "list_slots", json.RawMessage(`{"invalid":true}`))
	require.Error(t, err)
	require.EqualValues(t, 1, calls.Load())
	_, err = broker.Call(context.Background(), "massage", "book_slot", json.RawMessage(`{"slot_id":"slot"}`))
	require.ErrorIs(t, err, modules.ErrWriteDisabled)
	require.EqualValues(t, 1, calls.Load())
	p := principal()
	p.Permissions = []string{"zns.massage.access"}
	tools, err = registry.List(context.Background(), p)
	require.NoError(t, err)
	require.Empty(t, tools)
	require.Len(t, registry.CallerBindings(), 1)
}
func TestDynamicDiscoveryCacheIsPerUserAndPolicy(t *testing.T) {
	t.Parallel()
	manifest := example(t)
	method := manifest.Modules[0].Methods[0]
	method.ID = "list"
	method.Permissions = []string{}
	method.Path = "/read"
	method.InputSchema = json.RawMessage(`{"type":"object"}`)
	method.OutputSchema = json.RawMessage(`{"type":"object"}`)
	manifest.Modules = manifest.Modules[1:]
	var discoveries atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manifest" {
			_ = json.NewEncoder(w).Encode(manifest)
			return
		}
		discoveries.Add(1)
		assert.NotEmpty(t, r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(map[string]any{"methods": []modules.Method{method}})
	}))
	defer server.Close()
	registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		return []modules.Endpoint{endpoint(t, server.URL, "one")}, nil
	}), modules.Options{})
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(context.Background()))
	p := principal()
	for range 2 {
		tools, listErr := registry.List(context.Background(), p)
		require.NoError(t, listErr)
		require.Len(t, tools, 1)
	}
	require.EqualValues(t, 1, discoveries.Load())
	p.Subject = "another"
	_, err = registry.List(context.Background(), p)
	require.NoError(t, err)
	require.EqualValues(t, 2, discoveries.Load())
	p.PolicyRevision = "new"
	_, err = registry.List(context.Background(), p)
	require.NoError(t, err)
	require.EqualValues(t, 3, discoveries.Load())
	p.Permissions = nil
	tools, err := registry.List(context.Background(), p)
	require.NoError(t, err)
	require.Empty(t, tools)
	require.EqualValues(t, 3, discoveries.Load())
}
func TestRegistryRejectsReplicaConflictAndRemoval(t *testing.T) {
	t.Parallel()
	first := example(t)
	first.Modules = first.Modules[:1]
	second := first
	second.Revision = "different"
	server1 := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(first) }),
	)
	defer server1.Close()
	server2 := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(second) }),
	)
	defer server2.Close()
	endpoints := []modules.Endpoint{endpoint(t, server1.URL, "one")}
	registry, err := modules.NewRegistry(
		modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) { return endpoints, nil }),
		modules.Options{},
	)
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(context.Background()))
	endpoints = append(endpoints, endpoint(t, server2.URL, "two"))
	require.ErrorContains(t, registry.Refresh(context.Background()), "conflict")
	tools, err := registry.List(context.Background(), principal())
	require.NoError(t, err)
	require.Empty(t, tools)
	endpoints = nil
	require.NoError(t, registry.Refresh(context.Background()))
	require.ErrorIs(t, registry.CheckCaller(first.ServiceID), modules.ErrUnavailable)
}
func TestRegistryBlocksRemoteSchemasTraversalAndStaleCaller(t *testing.T) {
	t.Parallel()
	for _, mutation := range []func(*modules.Manifest){func(m *modules.Manifest) { m.Modules[0].Methods[0].Path = "/%2e%2e/private" }, func(m *modules.Manifest) {
		m.Modules[0].Methods[0].InputSchema = json.RawMessage(`{"$ref":"https://external.invalid/schema"}`)
	}} {
		manifest := example(t)
		manifest.Modules = manifest.Modules[:1]
		mutation(&manifest)
		server := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(manifest) }),
		)
		registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
			return []modules.Endpoint{endpoint(t, server.URL, "one")}, nil
		}), modules.Options{})
		require.NoError(t, err)
		require.Error(t, registry.Refresh(context.Background()))
		tools, err := registry.List(context.Background(), principal())
		require.NoError(t, err)
		require.Empty(t, tools)
		server.Close()
	}
	manifest := example(t)
	manifest.Modules = manifest.Modules[:1]
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(manifest) }),
	)
	defer server.Close()
	registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		return []modules.Endpoint{endpoint(t, server.URL, "one")}, nil
	}), modules.Options{FreshTTL: time.Millisecond})
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(context.Background()))
	time.Sleep(3 * time.Millisecond)
	require.Empty(t, registry.CallerBindings())
}
func TestDockerSourceTrustsOnlyNetworkBoundAlias(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/containers/json":
			_ = json.NewEncoder(w).
				Encode([]any{map[string]any{"Id": "trusted", "Labels": map[string]string{modules.ServiceLabel: "http://orders:8080/manifest"}}, map[string]any{"Id": "external", "Labels": map[string]string{modules.ServiceLabel: "http://evil.example:8080/manifest"}}})
		default:
			_ = json.NewEncoder(w).
				Encode(map[string]any{"Name": "/container", "State": map[string]bool{"Running": true}, "NetworkSettings": map[string]any{"Networks": map[string]any{"trusted-net": map[string]any{"IPAddress": "172.20.0.9", "Aliases": []string{"orders"}}}}})
		}
	}))
	defer server.Close()
	source := modules.DockerSource{BaseURL: server.URL, Network: "trusted-net"}
	endpoints, err := source.Discover(context.Background())
	require.NoError(t, err)
	require.Len(t, endpoints, 1)
	require.Equal(t, "trusted", endpoints[0].ContainerID)
	require.Equal(t, "172.20.0.9", endpoints[0].Address)
	source.Network = "other"
	endpoints, err = source.Discover(context.Background())
	require.NoError(t, err)
	require.Empty(t, endpoints)
}

func TestChangedDestinationCannotUseStaleCapability(t *testing.T) {
	t.Parallel()
	manifest := example(t)
	manifest.Modules = manifest.Modules[:1]
	good := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(manifest) }),
	)
	defer good.Close()
	failed := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }),
	)
	defer failed.Close()
	current := endpoint(t, good.URL, "one")
	registry, err := modules.NewRegistry(
		modules.SourceFunc(
			func(context.Context) ([]modules.Endpoint, error) { return []modules.Endpoint{current}, nil },
		),
		modules.Options{},
	)
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(context.Background()))
	tools, err := registry.List(context.Background(), principal())
	require.NoError(t, err)
	require.Len(t, tools, 1)
	current = endpoint(t, failed.URL, "one")
	require.Error(t, registry.Refresh(context.Background()))
	tools, err = registry.List(context.Background(), principal())
	require.NoError(t, err)
	require.Empty(t, tools)
	require.Empty(t, registry.CallerBindings())
}

func TestLiveBrokerRevocationPreventsDispatch(t *testing.T) {
	t.Parallel()
	manifest := example(t)
	manifest.Modules = manifest.Modules[:1]
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manifest" {
			_ = json.NewEncoder(w).Encode(manifest)
			return
		}
		reads.Add(1)
		var request struct {
			ID string `json:"operation_id"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			return
		}
		assert.Equal(t, "Bearer refreshed-token", r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).
			Encode(map[string]any{"status": "succeeded", "operation_id": request.ID, "result": map[string]any{"slots": []any{}}})
	}))
	defer server.Close()
	registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		return []modules.Endpoint{endpoint(t, server.URL, "one")}, nil
	}), modules.Options{})
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(context.Background()))
	current := principal()
	current.Token = "refreshed-token"
	broker := registry.BrokerLive(func(context.Context) (modules.Principal, error) { return current, nil }, "run", nil)
	_, err = broker.Call(context.Background(), "massage", "list_slots", json.RawMessage(`{"specialist_id":"andrey"}`))
	require.NoError(t, err)
	require.EqualValues(t, 1, reads.Load())
	current.Permissions = []string{"zns.massage.access"}
	_, err = broker.Call(context.Background(), "massage", "list_slots", json.RawMessage(`{"specialist_id":"andrey"}`))
	require.ErrorIs(t, err, modules.ErrForbidden)
	require.EqualValues(t, 1, reads.Load())
}

func TestTrustReadinessDistinguishesEmptyFromUnknown(t *testing.T) {
	t.Parallel()
	failed := false
	registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		if failed {
			return nil, errors.New("discovery offline")
		}
		return nil, nil
	}), modules.Options{FreshTTL: 5 * time.Millisecond})
	require.NoError(t, err)
	require.ErrorIs(t, registry.TrustReady(), modules.ErrUnavailable)
	require.NoError(t, registry.Refresh(context.Background()))
	require.NoError(t, registry.TrustReady())
	failed = true
	require.Error(t, registry.Refresh(context.Background()))
	require.ErrorIs(t, registry.TrustReady(), modules.ErrUnavailable)
	failed = false
	require.NoError(t, registry.Refresh(context.Background()))
	require.NoError(t, registry.TrustReady())
	time.Sleep(10 * time.Millisecond)
	require.ErrorIs(t, registry.TrustReady(), modules.ErrUnavailable)
}
func TestConfiguredManifestTimeout(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(time.Second):
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		return []modules.Endpoint{endpoint(t, server.URL, "one")}, nil
	}), modules.Options{RequestTimeout: 15 * time.Millisecond})
	require.NoError(t, err)
	start := time.Now()
	require.Error(t, registry.Refresh(context.Background()))
	require.Less(t, time.Since(start), 500*time.Millisecond)
	require.ErrorIs(t, registry.TrustReady(), modules.ErrUnavailable)
}
func TestReadArtifactUsesTrustedImportBoundary(t *testing.T) {
	t.Parallel()
	manifest := example(t)
	manifest.Modules = manifest.Modules[:1]
	manifest.Modules[0].Methods[0].ArtifactMIMETypes = []string{"image/png"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manifest" {
			_ = json.NewEncoder(w).Encode(manifest)
			return
		}
		var request struct {
			ID string `json:"operation_id"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			return
		}
		_ = json.NewEncoder(w).
			Encode(map[string]any{"status": "succeeded", "operation_id": request.ID, "result": map[string]any{"slots": []any{}}, "artifacts": []modules.ArtifactDescriptor{{DownloadPath: "/blobs/image", MIMEType: "image/png", SizeBytes: 50, SHA256: "digest"}}})
	}))
	defer server.Close()
	importer := func(ctx context.Context, p modules.Principal, d modules.ArtifactDescriptor, source modules.ArtifactSource) (json.RawMessage, error) {
		require.Equal(t, "user", p.Subject)
		require.Equal(t, "zns-chatbot", source.ServiceID)
		require.Equal(t, "run-artifact", source.RunID)
		require.False(t, source.ExternalDisclosure)
		require.Equal(t, "Bearer delegated-token", source.Headers.Get("Authorization"))
		require.Equal(t, "/blobs/image", d.DownloadPath)
		require.NoError(t, source.Authorize(ctx))
		return json.RawMessage(`{"artifact_id":"owned-handle"}`), nil
	}
	registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		return []modules.Endpoint{endpoint(t, server.URL, "one")}, nil
	}), modules.Options{ImportArtifact: importer})
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(context.Background()))
	result, err := registry.Broker(principal(), "run-artifact", nil).
		Call(context.Background(), "massage", "list_slots", json.RawMessage(`{"specialist_id":"andrey"}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"result":{"slots":[]},"artifacts":[{"artifact_id":"owned-handle"}]}`, string(result))
}

func TestConfiguredDynamicCacheCeiling(t *testing.T) {
	t.Parallel()
	manifest := example(t)
	method := manifest.Modules[0].Methods[0]
	manifest.Modules = manifest.Modules[1:]
	method.ID = "list"
	method.Path = "/read"
	method.Permissions = []string{}
	method.InputSchema = json.RawMessage("{}")
	method.OutputSchema = json.RawMessage("{}")
	var discoveries atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manifest" {
			_ = json.NewEncoder(w).Encode(manifest)
			return
		}
		discoveries.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"methods": []modules.Method{method}})
	}))
	defer server.Close()
	registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		return []modules.Endpoint{endpoint(t, server.URL, "one")}, nil
	}), modules.Options{DynamicCacheCeiling: time.Millisecond})
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(context.Background()))
	_, err = registry.List(context.Background(), principal())
	require.NoError(t, err)
	require.EqualValues(t, 1, discoveries.Load())
	time.Sleep(5 * time.Millisecond)
	_, err = registry.List(context.Background(), principal())
	require.NoError(t, err)
	require.EqualValues(t, 2, discoveries.Load())
}

func TestArtifactRejectsRetiredDestinationBeforeGET(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"container", "address"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			checkRetiredArtifactDestination(t, change)
		})
	}
}
func TestArtifactRefreshesCredentialsBeforeGETAndClonesRequestHeaders(t *testing.T) {
	t.Parallel()
	manifest := example(t)
	manifest.Modules = manifest.Modules[:1]
	manifest.Modules[0].Methods[0].ArtifactMIMETypes = []string{"image/png"}
	var gets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manifest" {
			_ = json.NewEncoder(w).Encode(manifest)
			return
		}
		if r.URL.Path == "/blobs/image" {
			gets.Add(1)
			assert.Equal(t, "Bearer rotated-one", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte("blob"))
			return
		}
		var request struct {
			ID string `json:"operation_id"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			return
		}
		_ = json.NewEncoder(w).
			Encode(map[string]any{"status": "succeeded", "operation_id": request.ID, "result": map[string]any{"slots": []any{}}, "artifacts": []modules.ArtifactDescriptor{{DownloadPath: "/blobs/image", MIMEType: "image/png", SizeBytes: 4, SHA256: "digest"}}})
	}))
	defer server.Close()
	current := principal()
	importer := func(ctx context.Context, _ modules.Principal, _ modules.ArtifactDescriptor, source modules.ArtifactSource) (json.RawMessage, error) {
		current.Token = "rotated-one"
		require.NoError(t, source.Authorize(ctx))
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.BaseURL+"/blobs/image", nil)
		require.NoError(t, err)
		request.Header = source.Headers.Clone()
		response, err := source.Client.Do(request)
		require.NoError(t, err)
		defer func() { _ = response.Body.Close() }()
		_, err = io.ReadAll(response.Body)
		require.NoError(t, err)
		current.Token = "rotated-two"
		require.NoError(t, source.Authorize(ctx))
		require.Equal(t, "Bearer rotated-two", source.Headers.Get("Authorization"))
		require.Equal(t, "Bearer rotated-one", request.Header.Get("Authorization"))
		return json.RawMessage(`{"artifact_id":"owned"}`), nil
	}
	registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		return []modules.Endpoint{endpoint(t, server.URL, "one")}, nil
	}), modules.Options{ImportArtifact: importer})
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(context.Background()))
	result, err := registry.BrokerLive(func(context.Context) (modules.Principal, error) { return current, nil }, "run", nil).
		Call(context.Background(), "massage", "list_slots", json.RawMessage(`{"specialist_id":"andrey"}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"result":{"slots":[]},"artifacts":[{"artifact_id":"owned"}]}`, string(result))
	require.EqualValues(t, 1, gets.Load())
}

func TestCallerIdentitiesPersistBeforePublicationAndFailureClosesTrust(t *testing.T) {
	t.Parallel()
	manifest := example(t)
	manifest.Modules = manifest.Modules[:1]
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(manifest)
	}))
	defer server.Close()
	persistErr := errors.New("identity persistence unavailable")
	var registry *modules.Registry
	var fail bool
	var calls int
	var err error
	registry, err = modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		return []modules.Endpoint{endpoint(t, server.URL, "one")}, nil
	}), modules.Options{OnCallerBindings: func(_ context.Context, bindings []modules.CallerBinding) error {
		calls++
		require.Len(t, bindings, 1)
		require.Equal(t, manifest.ServiceID, bindings[0].ServiceID)
		require.Equal(t, manifest.Caller.Auth, bindings[0].Auth)
		require.Equal(t, manifest.Caller.Permissions, bindings[0].Permissions)
		if calls == 1 {
			require.Empty(t, registry.CallerBindings())
			require.ErrorIs(t, registry.TrustReady(), modules.ErrUnavailable)
		}
		bindings[0].Permissions[0] = "cannot-escalate"
		if fail {
			return persistErr
		}
		return nil
	}})
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(context.Background()))
	require.NoError(t, registry.TrustReady())
	require.Equal(t, manifest.Caller.Permissions, registry.CallerBindings()[0].Permissions)
	tools, err := registry.List(context.Background(), principal())
	require.NoError(t, err)
	require.NotEmpty(t, tools)

	fail = true
	require.ErrorIs(t, registry.Refresh(context.Background()), persistErr)
	require.ErrorIs(t, registry.TrustReady(), modules.ErrUnavailable)
	require.Empty(t, registry.CallerBindings())
	tools, err = registry.List(context.Background(), principal())
	require.NoError(t, err)
	require.Empty(t, tools)

	fail = false
	require.NoError(t, registry.Refresh(context.Background()))
	require.NoError(t, registry.TrustReady())
	require.Len(t, registry.CallerBindings(), 1)
	require.Equal(t, 3, calls)
}

func TestManifestIdentityReplacementRetiresOldCallerAndCatalog(t *testing.T) {
	t.Parallel()
	manifest := example(t)
	manifest.Modules = manifest.Modules[:1]
	oldID := manifest.ServiceID
	oldSubject := manifest.Caller.Auth.Subject
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(manifest)
	}))
	defer server.Close()
	var persisted []modules.CallerBinding
	registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		return []modules.Endpoint{endpoint(t, server.URL, "same-container")}, nil
	}), modules.Options{OnCallerBindings: func(_ context.Context, bindings []modules.CallerBinding) error {
		persisted = bindings
		return nil
	}})
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(context.Background()))
	require.NoError(t, registry.CheckCaller(oldID))
	manifest.ServiceID = "replacement"
	manifest.Caller.Auth.Subject = "replacement-subject"
	require.NoError(t, registry.Refresh(context.Background()))
	require.NoError(t, registry.TrustReady())
	require.ErrorIs(t, registry.CheckCaller(oldID), modules.ErrUnavailable)
	require.NoError(t, registry.CheckCaller("replacement"))
	bindings := registry.CallerBindings()
	require.Len(t, bindings, 1)
	require.Equal(t, "replacement", bindings[0].ServiceID)
	require.NotEqual(t, oldSubject, bindings[0].Auth.Subject)
	require.Len(t, persisted, 1)
	require.Equal(t, "replacement", persisted[0].ServiceID)
	tools, err := registry.List(context.Background(), principal())
	require.NoError(t, err)
	require.Len(t, tools, 1)
	require.Equal(t, "replacement", tools[0].ServiceID)
}

func TestIdentityReplacementKeepsAnotherRegisteredReplica(t *testing.T) {
	t.Parallel()
	for _, replacementFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "replacement-first", false: "replica-first"}[replacementFirst], func(t *testing.T) {
			t.Parallel()
			original := example(t)
			original.Modules = original.Modules[:1]
			replacement := original
			changed := false
			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if changed {
					_ = json.NewEncoder(w).Encode(replacement)
				} else {
					_ = json.NewEncoder(w).Encode(original)
				}
			}))
			defer first.Close()
			second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(original)
			}))
			defer second.Close()
			registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
				endpoints := []modules.Endpoint{
					endpoint(t, second.URL, "replica"),
					endpoint(t, first.URL, "replacement"),
				}
				if replacementFirst {
					endpoints[0], endpoints[1] = endpoints[1], endpoints[0]
				}
				return endpoints, nil
			}), modules.Options{})
			require.NoError(t, err)
			require.NoError(t, registry.Refresh(context.Background()))
			replacement.ServiceID = "replacement"
			changed = true
			require.NoError(t, registry.Refresh(context.Background()))
			require.NoError(t, registry.CheckCaller(original.ServiceID))
			require.NoError(t, registry.CheckCaller("replacement"))
			require.Len(t, registry.CallerBindings(), 2)
		})
	}
}

func TestMethodDeadlineIsIndependentOfManifestDeadline(t *testing.T) {
	t.Parallel()
	manifest := example(t)
	manifest.Modules = manifest.Modules[:1]
	manifest.Modules[0].Methods = manifest.Modules[0].Methods[:1]
	manifest.Modules[0].Methods[0].TimeoutMS = 1000
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manifest" {
			_ = json.NewEncoder(w).Encode(manifest)
			return
		}
		var request struct {
			OperationID string `json:"operation_id"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"operation_id": request.OperationID,
			"status":       "succeeded",
			"result":       map[string]any{"slots": []any{}},
		})
	}))
	defer server.Close()
	registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		return []modules.Endpoint{endpoint(t, server.URL, "one")}, nil
	}), modules.Options{RequestTimeout: 100 * time.Millisecond})
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(context.Background()))
	broker := registry.Broker(principal(), "run-one", nil)
	result, err := broker.Call(
		context.Background(),
		"massage",
		"list_slots",
		json.RawMessage(`{"specialist_id":"andrey"}`),
	)
	require.NoError(t, err)
	require.JSONEq(t, `{"slots":[]}`, string(result))

	manifest.Modules[0].Methods[0].TimeoutMS = 30
	require.NoError(t, registry.Refresh(context.Background()))
	_, err = broker.Call(context.Background(), "massage", "list_slots", json.RawMessage(`{"specialist_id":"andrey"}`))
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestMethodCallPropagatesCancellationAfterDelivery(t *testing.T) {
	t.Parallel()
	ctx, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	manifest := example(t)
	manifest.Modules = manifest.Modules[:1]
	manifest.Modules[0].Methods = manifest.Modules[0].Methods[:1]
	manifest.Modules[0].Methods[0].TimeoutMS = 10000
	started, cancelled := make(chan struct{}), make(chan struct{})
	stopHandler := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manifest" {
			_ = json.NewEncoder(w).Encode(manifest)
			return
		}
		_, bodyErr := io.Copy(io.Discard, r.Body)
		if !assert.NoError(t, bodyErr) {
			return
		}
		close(started)
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-stopHandler:
		}
	}))
	defer func() {
		close(stopHandler)
		server.Close()
	}()
	registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		return []modules.Endpoint{endpoint(t, server.URL, "one")}, nil
	}), modules.Options{})
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(ctx))
	broker := registry.Broker(principal(), "run-one", nil)
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, callErr := broker.Call(callCtx, "massage", "list_slots", json.RawMessage(`{"specialist_id":"andrey"}`))
		result <- callErr
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("method request did not reach server")
	}
	cancel()
	select {
	case err = <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-ctx.Done():
		t.Fatal("method call did not stop after cancellation")
	}
	select {
	case <-cancelled:
	case <-ctx.Done():
		t.Fatal("server did not observe request cancellation")
	}
}

func checkRetiredArtifactDestination(t *testing.T, change string) {
	t.Helper()
	manifest := example(t)
	manifest.Modules = manifest.Modules[:1]
	manifest.Modules[0].Methods[0].ArtifactMIMETypes = []string{"image/png"}
	var blobGets atomic.Int32
	var leakedCredentials atomic.Int32
	original := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manifest" {
			_ = json.NewEncoder(w).Encode(manifest)
			return
		}
		if r.URL.Path == "/blobs/image" {
			blobGets.Add(1)
			if r.Header.Get("Authorization") != "" {
				leakedCredentials.Add(1)
			}
			_, _ = w.Write([]byte("blob"))
			return
		}
		var request struct {
			ID string `json:"operation_id"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			return
		}
		_ = json.NewEncoder(w).
			Encode(map[string]any{"status": "succeeded", "operation_id": request.ID, "result": map[string]any{"slots": []any{}}, "artifacts": []modules.ArtifactDescriptor{{DownloadPath: "/blobs/image", MIMEType: "image/png", SizeBytes: 4, SHA256: "digest"}}})
	}))
	defer original.Close()
	current := endpoint(t, original.URL, "original")
	replacement := current
	replacement.ContainerID = "replacement"
	if change == "address" {
		parsed, err := url.Parse(original.URL)
		require.NoError(t, err)
		listener, err := net.Listen("tcp", "127.0.0.2:"+parsed.Port())
		require.NoError(t, err)
		newServer := httptest.NewUnstartedServer(
			http.HandlerFunc(
				func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(manifest) },
			),
		)
		newServer.Listener = listener
		newServer.Start()
		defer newServer.Close()
		replacement.ContainerID = current.ContainerID
		replacement.Address = "127.0.0.2"
	}
	var registry *modules.Registry
	importer := func(ctx context.Context, _ modules.Principal, _ modules.ArtifactDescriptor, source modules.ArtifactSource) (json.RawMessage, error) {
		current = replacement
		require.NoError(t, registry.Refresh(ctx))
		if err := source.Authorize(ctx); err != nil {
			return nil, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.BaseURL+"/blobs/image", nil)
		if err != nil {
			return nil, err
		}
		request.Header = source.Headers.Clone()
		response, err := source.Client.Do(request)
		if err != nil {
			return nil, err
		}
		defer func() { _ = response.Body.Close() }()
		return json.RawMessage(`{"artifact_id":"unexpected"}`), nil
	}
	var err error
	registry, err = modules.NewRegistry(
		modules.SourceFunc(
			func(context.Context) ([]modules.Endpoint, error) { return []modules.Endpoint{current}, nil },
		),
		modules.Options{ImportArtifact: importer},
	)
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(context.Background()))
	_, err = registry.Broker(principal(), "run", nil).
		Call(context.Background(), "massage", "list_slots", json.RawMessage(`{"specialist_id":"andrey"}`))
	require.ErrorIs(t, err, modules.ErrUnavailable)
	require.Zero(t, blobGets.Load())
	require.Zero(t, leakedCredentials.Load())
}
