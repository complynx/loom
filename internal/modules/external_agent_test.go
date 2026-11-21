package modules_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/modules"
)

func TestExternalAgentReadsOnlyMarkedMethodsWithoutArtifacts(t *testing.T) {
	t.Parallel()
	manifest := example(t)
	manifest.Modules = manifest.Modules[:1]
	marked := manifest.Modules[0].Methods[0]
	marked.ExternalAgent = true
	marked.ArtifactMIMETypes = []string{"image/png"}
	unmarked := manifest.Modules[0].Methods[0]
	unmarked.ID = "list_private_slots"
	write := manifest.Modules[0].Methods[1]
	write.ExternalAgent = true
	manifest.Modules[0].Methods = []modules.Method{marked, unmarked, write}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manifest" {
			_ = json.NewEncoder(w).Encode(manifest)
			return
		}
		var request struct {
			ID    string `json:"operation_id"`
			RunID string `json:"run_id"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			return
		}
		assert.Equal(t, "execution-one", request.RunID)
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "succeeded", "operation_id": request.ID, "result": map[string]any{"slots": []any{}},
			"artifacts": []modules.ArtifactDescriptor{
				{DownloadPath: "/blobs/image", MIMEType: "image/png", SizeBytes: 50, SHA256: "digest"},
			},
		})
	}))
	defer server.Close()
	importer := func(context.Context, modules.Principal, modules.ArtifactDescriptor, modules.ArtifactSource) (json.RawMessage, error) {
		t.Error("an external read must not import artifacts")
		return nil, nil
	}
	registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		return []modules.Endpoint{endpoint(t, server.URL, "one")}, nil
	}), modules.Options{ImportArtifact: importer})
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(context.Background()))

	all, err := registry.List(context.Background(), principal())
	require.NoError(t, err)
	require.Len(t, all, 2)
	external, err := registry.ListExternal(context.Background(), principal())
	require.NoError(t, err)
	require.Len(t, external, 1)
	require.Equal(t, "list_slots", external[0].Method)

	broker := registry.ExternalBroker(
		func(context.Context) (modules.Principal, error) { return principal(), nil },
		"execution-one",
	)
	arguments := json.RawMessage(`{"specialist_id":"andrey"}`)
	result, err := broker.Call(context.Background(), "massage", "list_slots", arguments)
	require.NoError(t, err)
	require.JSONEq(t, `{"slots":[]}`, string(result))
	_, err = broker.Call(context.Background(), "massage", "list_private_slots", arguments)
	require.ErrorIs(t, err, modules.ErrUnavailable)
	_, err = broker.Call(context.Background(), "massage", "book_slot", json.RawMessage(`{"slot_id":"slot"}`))
	require.ErrorIs(t, err, modules.ErrWriteDisabled)
	require.EqualValues(t, 1, calls.Load())

	_, err = registry.DescribeExternal(context.Background(), principal(), "massage", "list_slots")
	require.NoError(t, err)
	for _, method := range []string{"list_private_slots", "book_slot"} {
		_, err = registry.DescribeExternal(context.Background(), principal(), "massage", method)
		require.ErrorIs(t, err, modules.ErrUnavailable)
	}
}

// Loom sends the agent's token only to a service that declared a token endpoint,
// together with the secret that proves that the request comes from Loom.
func TestExternalTokenServiceGetsSecretAndSubjectToken(t *testing.T) {
	t.Parallel()
	serve := func(manifest modules.Manifest, requests *atomic.Int32) *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/manifest" {
				_ = json.NewEncoder(w).Encode(manifest)
				return
			}
			requests.Add(1)
			var request struct {
				Version int    `json:"protocol_version"`
				Subject string `json:"subject_token"`
			}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			assert.Equal(t, "/external-agent/token", r.URL.Path)
			assert.Equal(t, "Bearer loom-secret", r.Header.Get("Authorization"))
			assert.Equal(t, 1, request.Version)
			assert.Equal(t, "agent-token", request.Subject)
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "service-token", "expires_in": 600})
		}))
		t.Cleanup(server.Close)
		return server
	}
	var requests, strayRequests atomic.Int32
	issuing := example(t)
	issuing.Summary = "Chat client."
	issuing.Modules = []modules.Module{}
	issuing.ExternalAgentTokens = &modules.ExternalAgentTokens{Path: "/external-agent/token"}
	plain := example(t)
	plain.ServiceID = "plain-service"
	plain.Modules = []modules.Module{}
	traversal := example(t)
	traversal.ServiceID = "traversal-service"
	traversal.Modules = []modules.Module{}
	traversal.ExternalAgentTokens = &modules.ExternalAgentTokens{Path: "/tokens/../manifest"}
	registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		return []modules.Endpoint{
			endpoint(t, serve(issuing, &requests).URL, "one"),
			endpoint(t, serve(plain, &strayRequests).URL, "two"),
			endpoint(t, serve(traversal, &strayRequests).URL, "three"),
		}, nil
	}), modules.Options{})
	require.NoError(t, err)
	require.Error(t, registry.Refresh(context.Background()), "a token path with traversal must be refused")
	require.Equal(
		t,
		[]modules.TokenService{{ServiceID: issuing.ServiceID, Summary: "Chat client."}},
		registry.ExternalTokenServices(),
	)

	token, expiresIn, err := registry.ExternalToken(
		context.Background(),
		issuing.ServiceID,
		"loom-secret",
		"agent-token",
	)
	require.NoError(t, err)
	require.Equal(t, "service-token", token)
	require.EqualValues(t, 600, *expiresIn)
	for _, refused := range [][2]string{
		{"plain-service", "loom-secret"}, {"traversal-service", "loom-secret"}, {"unknown", "loom-secret"},
		{issuing.ServiceID, ""},
	} {
		_, _, err = registry.ExternalToken(context.Background(), refused[0], refused[1], "agent-token")
		require.ErrorIs(t, err, modules.ErrUnavailable, refused[0])
	}
	require.EqualValues(t, 1, requests.Load())
	require.Zero(t, strayRequests.Load(), "the agent's token must not reach an undeclared service")
}
