package acceptance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/acceptance"
	"github.com/complynx/loom/internal/modules"
	"github.com/complynx/loom/internal/operations"
)

const readToken = "isolated-read-token"
const writeToken = "isolated-write-token"
const moduleFixture = "fixture"
const methodWrite = "write"

func fixtureServer(t *testing.T, path string) *httptest.Server {
	t.Helper()
	service, err := acceptance.New(path, readToken, writeToken)
	require.NoError(t, err)
	server := httptest.NewServer(service.Handler())
	t.Cleanup(server.Close)
	return server
}
func fixturePrincipal(token string) modules.Principal {
	return modules.Principal{Issuer: "https://qa.invalid", Subject: "fixture-user", Token: token,
		Permissions: []string{"qa.access", "qa.read", "qa.write"}}
}
func fixtureRegistry(t *testing.T, server *httptest.Server, importer modules.ArtifactImporter) *modules.Registry {
	t.Helper()
	address, err := url.Parse(server.URL)
	require.NoError(t, err)
	registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		return []modules.Endpoint{
			{
				ContainerID: "qa-container",
				URI:         "http://registered-fixture:" + address.Port() + "/manifest",
				Address:     "127.0.0.1",
			},
		}, nil
	}), modules.Options{ImportArtifact: importer})
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(context.Background()))
	return registry
}
func post(t *testing.T, server *httptest.Server, path, token string, body []byte) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		server.URL+path,
		bytes.NewReader(body),
	)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func TestRegistryDiscoversFiltersReadsAndImportsBlob(t *testing.T) {
	t.Parallel()
	server := fixtureServer(t, filepath.Join(t.TempDir(), "state.json"))
	imported := 0
	registry := fixtureRegistry(
		t,
		server,
		func(ctx context.Context, principal modules.Principal, descriptor modules.ArtifactDescriptor, source modules.ArtifactSource) (json.RawMessage, error) {
			require.Equal(t, readToken, principal.Token)
			require.NoError(t, source.Authorize(ctx))
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.BaseURL+descriptor.DownloadPath, nil)
			require.NoError(t, err)
			request.Header = source.Headers.Clone()
			response, err := source.Client.Do(request)
			require.NoError(t, err)
			defer func() { _ = response.Body.Close() }()
			require.Equal(t, http.StatusOK, response.StatusCode)
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Equal(t, "Acceptance blob. Original bytes bypass JavaScript.\n", string(body))
			digest := sha256.Sum256(body)
			require.Equal(t, hex.EncodeToString(digest[:]), descriptor.SHA256)
			require.EqualValues(t, len(body), descriptor.SizeBytes)
			require.Equal(t, "text/plain", descriptor.MIMEType)
			require.True(t, source.ExternalDisclosure)
			require.NoError(t, source.Authorize(ctx))
			imported++
			return json.RawMessage(`{"handle_id":"qa-blob"}`), nil
		},
	)
	principal := fixturePrincipal(readToken)
	principal.Permissions = []string{"qa.access", "qa.read"}
	tools, err := registry.List(context.Background(), principal)
	require.NoError(t, err)
	require.Len(t, tools, 2)
	methods := []string{tools[0].Method, tools[1].Method}
	require.ElementsMatch(t, []string{"read", "lookup"}, methods)
	actions, err := registry.ListActions(context.Background(), principal)
	require.NoError(t, err)
	require.Empty(t, actions)
	broker := registry.Broker(principal, "qa-run", nil)
	for _, tool := range tools {
		result, callErr := broker.Call(
			context.Background(),
			tool.Module,
			tool.Method,
			json.RawMessage(`{"query":"order"}`),
		)
		require.NoError(t, callErr)
		require.Contains(t, string(result), "Fixture read: order")
		require.Contains(t, string(result), "qa-blob")
	}
	require.Equal(t, 2, imported)
	_, err = broker.Call(
		context.Background(),
		moduleFixture,
		"read",
		json.RawMessage(`{"query":"order","unexpected":true}`),
	)
	require.Error(t, err)
	denied := principal
	denied.Permissions = nil
	tools, err = registry.List(context.Background(), denied)
	require.NoError(t, err)
	require.Empty(t, tools)
}

func fixtureOperation(
	t *testing.T,
	registry *modules.Registry,
	principal modules.Principal,
	id string,
) operations.Operation {
	t.Helper()
	descriptor, err := registry.Describe(context.Background(), principal, moduleFixture, methodWrite)
	require.NoError(t, err)
	return operations.Operation{ID: id, RunID: "qa-run", Module: moduleFixture,
		Method: methodWrite, Mode: "service_id", Arguments: json.RawMessage(`{"query":"chosen value"}`),
		Binding: operations.Binding{Issuer: principal.Issuer, Subject: principal.Subject, Actor: principal.Actor,
			OrgID: principal.Organization, ContractHash: descriptor.ContractHash}}
}
func TestConfirmedWriteSurvivesLostReplyAndServiceRestart(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.json")
	server := fixtureServer(t, path)
	registry := fixtureRegistry(t, server, nil)
	principal := fixturePrincipal(writeToken)
	backend := modules.WriteBackend{Registry: registry, Principal: principal}
	operation := fixtureOperation(t, registry, principal, "stable-operation")
	preparation, err := backend.Prepare(context.Background(), operation)
	require.NoError(t, err)
	require.NotEmpty(t, preparation.Token)
	duplicate, err := backend.Prepare(context.Background(), operation)
	require.NoError(t, err)
	require.Equal(t, preparation.Token, duplicate.Token)
	operation.Preparation = preparation
	changed := operation
	changed.Preparation.Token = "wrong-confirmation"
	_, err = backend.Execute(context.Background(), changed)
	require.Error(t, err)
	status, err := backend.Status(context.Background(), operation)
	require.NoError(t, err)
	require.Equal(t, "unknown", status.State)
	response := post(t, server, "/qa/faults", writeToken, []byte(`{"drop_next_write_reply":true}`))
	require.Equal(t, http.StatusNoContent, response.StatusCode)
	_, err = backend.Execute(context.Background(), operation)
	require.Error(t, err)
	status, err = backend.Status(context.Background(), operation)
	require.NoError(t, err)
	require.Equal(t, "succeeded", status.State)
	require.JSONEq(t, `{"value":"chosen value"}`, string(status.Output))
	server.Close()
	restarted := fixtureServer(t, path)
	registry = fixtureRegistry(t, restarted, nil)
	backend.Registry = registry
	status, err = backend.Status(context.Background(), operation)
	require.NoError(t, err)
	require.Equal(t, "succeeded", status.State)
	replay, err := backend.Execute(context.Background(), operation)
	require.NoError(t, err)
	require.Equal(t, status, replay)
	changed = operation
	changed.Preparation.Arguments = json.RawMessage(`{"query":"another value"}`)
	_, err = backend.Execute(context.Background(), changed)
	require.Error(t, err)
	status, err = backend.Status(context.Background(), operation)
	require.NoError(t, err)
	require.JSONEq(t, `{"value":"chosen value"}`, string(status.Output))
}

func TestFixtureRejectsReadTokenWritesAndInvalidArguments(t *testing.T) {
	t.Parallel()
	server := fixtureServer(t, filepath.Join(t.TempDir(), "state.json"))
	registry := fixtureRegistry(t, server, nil)
	principal := fixturePrincipal(readToken)
	backend := modules.WriteBackend{Registry: registry, Principal: principal}
	operation := fixtureOperation(t, registry, principal, "denied-operation")
	_, err := backend.Prepare(context.Background(), operation)
	require.Error(t, err)
	response := post(t, server, "/qa/faults", readToken, []byte(`{"drop_next_write_reply":true}`))
	require.Equal(t, http.StatusForbidden, response.StatusCode)
	for _, arguments := range []string{`{}`, `{"query":null}`, `{"query":"x","extra":true}`} {
		body := []byte(`{"protocol_version":1,"operation_id":"invalid","arguments":` + arguments + `}`)
		response = post(t, server, "/read", readToken, body)
		require.Equal(t, http.StatusUnprocessableEntity, response.StatusCode)
	}
}
