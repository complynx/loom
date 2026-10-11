package api_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/api"
	"github.com/complynx/loom/internal/telemetry"
)

func TestOpenAPIBillingAndSessionContracts(t *testing.T) {
	t.Parallel()
	response := request(api.New(nil, authFunc(allowed), api.Options{}), "GET", "/openapi.json", "", "", "")
	require.Equal(t, http.StatusOK, response.Code)
	var document map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &document))
	paths := document["paths"].(map[string]any)
	billing := paths["/v1/admin/billing/{id}/adjustments"].(map[string]any)["post"].(map[string]any)
	properties := publicSchemaProperties(t, document, billing["requestBody"])
	require.Contains(t, properties, "amount_microcredits")
	require.Contains(t, properties, "reason")
	require.NotContains(t, properties, "Amount")
	session := paths["/v1/auth/sessions"].(map[string]any)["post"].(map[string]any)
	require.Contains(t, session, "requestBody")
	responses := session["responses"].(map[string]any)
	properties = publicSchemaProperties(t, document, responses["201"])
	require.Contains(t, properties, "auth_session_id")
	require.Contains(t, properties, "expires_at")
	require.Contains(t, properties, "version")
	reauthorize := paths["/v1/auth/sessions/{id}/reauthorize"].(map[string]any)["post"].(map[string]any)
	properties = publicSchemaProperties(t, document, reauthorize["requestBody"])
	require.Contains(t, properties, "version")
}

func TestOpenAPIRunWaitAndCollectionResponses(t *testing.T) {
	t.Parallel()
	response := request(api.New(nil, authFunc(allowed), api.Options{}), "GET", "/openapi.json", "", "", "")
	require.Equal(t, http.StatusOK, response.Code)
	var document map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &document))
	paths := document["paths"].(map[string]any)
	run := paths["/v1/runs"].(map[string]any)["post"].(map[string]any)
	responses := run["responses"].(map[string]any)
	require.Equal(
		t,
		publicSchemaProperties(t, document, responses["200"]),
		publicSchemaProperties(t, document, responses["202"]),
	)
	for _, route := range []struct{ path, method, property string }{
		{"/v1/models", "get", "models"},
		{"/v1/chats/{id}/history", "get", "events"},
		{"/v1/admin/runs/{id}/steps/{step}/reconcile", "post", "unresolved_steps"},
	} {
		operation := paths[route.path].(map[string]any)[route.method].(map[string]any)
		responses := operation["responses"].(map[string]any)
		require.Contains(t, publicSchemaProperties(t, document, responses["200"]), route.property)
	}
}

func TestOpenAPIHistoryCursorsAndImageUpload(t *testing.T) {
	t.Parallel()
	response := request(api.New(nil, authFunc(allowed), api.Options{}), "GET", "/openapi.json", "", "", "")
	var document map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &document))
	paths := document["paths"].(map[string]any)
	for _, route := range []struct {
		path       string
		parameters []string
	}{
		{"/v1/chats/{id}/history", []string{"after", "limit"}},
		{"/v1/runs/{id}/events", []string{"after", "Last-Event-ID"}},
	} {
		operation := paths[route.path].(map[string]any)["get"].(map[string]any)
		names := map[string]bool{}
		for _, value := range operation["parameters"].([]any) {
			parameter := value.(map[string]any)
			names[parameter["name"].(string)] = true
		}
		for _, name := range route.parameters {
			require.True(t, names[name], name)
		}
	}
	upload := paths["/v1/artifacts"].(map[string]any)["post"].(map[string]any)
	content := upload["requestBody"].(map[string]any)["content"].(map[string]any)
	for _, mime := range []string{"application/octet-stream", "image/png", "image/jpeg", "image/gif", "image/webp"} {
		require.Contains(t, content, mime)
	}
}

func publicSchemaProperties(t *testing.T, document map[string]any, value any) map[string]any {
	t.Helper()
	require.NotNil(t, value)
	schema := value.(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if ref, ok := schema["$ref"].(string); ok {
		schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
		schema = schemas[strings.TrimPrefix(ref, "#/components/schemas/")].(map[string]any)
	}
	return schema["properties"].(map[string]any)
}

func TestHTTPErrorRequestIDMatchesHeaderAndLog(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	observer := telemetry.New(slog.New(slog.NewJSONHandler(&logs, nil)))
	handler := api.New(nil, authFunc(allowed), api.Options{})
	response := request(observer.HTTP(handler), "POST", "/v1/auth/sessions", `{"unknown":true}`, "application/json", "")
	require.Equal(t, http.StatusUnprocessableEntity, response.Code)
	var body struct {
		Error struct {
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.NotEmpty(t, body.Error.RequestID)
	require.Equal(t, response.Header().Get("X-Request-ID"), body.Error.RequestID)
	require.Contains(t, logs.String(), body.Error.RequestID)
	response = request(handler, "POST", "/v1/auth/sessions", `{"unknown":true}`, "application/json", "")
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.NotEmpty(t, body.Error.RequestID)
	require.Equal(t, response.Header().Get("X-Request-ID"), body.Error.RequestID)
}
