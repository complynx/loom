package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/api"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/identity"
)

type externalService struct{ calls []string }

func (s *externalService) Tools(context.Context, domain.ExternalCaller) ([]domain.ExternalTool, error) {
	return []domain.ExternalTool{
		{Name: "script", Description: "run", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}, nil
}

func (s *externalService) Call(
	_ context.Context,
	caller domain.ExternalCaller,
	name string,
	arguments json.RawMessage,
) (json.RawMessage, bool) {
	s.calls = append(s.calls, caller.Principal.Subject+" "+name+" "+string(arguments))
	if name != "script" {
		return json.RawMessage(`{"error":"unknown_tool"}`), true
	}
	return json.RawMessage(`{"ok":true}`), false
}

func externalHandler(service *externalService, options api.Options) http.Handler {
	options.ExternalAgent = &api.ExternalAgentOptions{
		Service: service,
		Authenticate: func(_ context.Context, r *http.Request) (domain.ExternalCaller, error) {
			if r.Header.Get("Authorization") != "Bearer user-token" {
				return domain.ExternalCaller{}, identity.ErrUnauthorized
			}
			return domain.ExternalCaller{
				Principal: domain.Principal{Subject: "alice", OrgID: "org-a", ExpiresAt: time.Now().Add(time.Hour)},
				ClientID:  "agent",
				Token:     "user-token",
			}, nil
		},
		Resource: "https://loom.example.com/mcp",
		Issuer:   "https://id.example.com",
		Scopes:   []string{"openid", "urn:example:aud"},
		Services: true,
	}
	return api.New(nil, authFunc(allowed), options)
}

func mcpRequest(handler http.Handler, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestMCPEndpointAuthorizesAndServesTools(t *testing.T) {
	t.Parallel()
	service := &externalService{}
	handler := externalHandler(service, api.Options{})

	denied := mcpRequest(handler, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	require.Equal(t, http.StatusUnauthorized, denied.Code)
	require.Equal(
		t,
		`Bearer resource_metadata="https://loom.example.com/.well-known/oauth-protected-resource/mcp", scope="openid urn:example:aud"`,
		denied.Header().Get("WWW-Authenticate"),
	)
	metadata := request(handler, "GET", "/.well-known/oauth-protected-resource/mcp", "", "", "")
	require.Equal(t, http.StatusOK, metadata.Code)
	require.JSONEq(t, `{
		"resource":"https://loom.example.com/mcp",
		"authorization_servers":["https://id.example.com"],
		"bearer_methods_supported":["header"],
		"scopes_supported":["openid","urn:example:aud"]
	}`, metadata.Body.String())

	initialize := mcpRequest(
		handler,
		"user-token",
		`{"jsonrpc":"2.0","id":"a","method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
	)
	require.Equal(t, http.StatusOK, initialize.Code)
	var initialized struct {
		ID     string `json:"id"`
		Result struct {
			ProtocolVersion string         `json:"protocolVersion"`
			Capabilities    map[string]any `json:"capabilities"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(initialize.Body.Bytes(), &initialized))
	require.Equal(t, "a", initialized.ID)
	require.Equal(t, "2025-06-18", initialized.Result.ProtocolVersion)
	require.Contains(t, initialized.Result.Capabilities, "tools")

	notification := mcpRequest(handler, "user-token", `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	require.Equal(t, http.StatusAccepted, notification.Code)
	require.Empty(t, notification.Body.String())

	list := mcpRequest(handler, "user-token", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	require.JSONEq(
		t,
		`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"script","description":"run","inputSchema":{"type":"object"}}]}}`,
		list.Body.String(),
	)

	call := mcpRequest(
		handler,
		"user-token",
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"script","arguments":{"code":"return 1"}}}`,
	)
	require.JSONEq(
		t,
		`{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"{\"ok\":true}"}],"isError":false}}`,
		call.Body.String(),
	)
	failed := mcpRequest(
		handler,
		"user-token",
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"other"}}`,
	)
	require.Contains(t, failed.Body.String(), `"isError":true`)
	require.Equal(t, []string{`alice script {"code":"return 1"}`, "alice other {}"}, service.calls)
}

func TestMCPEndpointRejectsInvalidExchanges(t *testing.T) {
	t.Parallel()
	service := &externalService{}
	handler := externalHandler(service, api.Options{})
	for name, tc := range map[string]struct {
		body   string
		status int
		code   float64
	}{
		"unknown method":  {`{"jsonrpc":"2.0","id":1,"method":"resources/list"}`, http.StatusOK, -32601},
		"missing tool":    {`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{}}`, http.StatusOK, -32602},
		"initialize type": {`{"jsonrpc":"2.0","id":1,"method":"initialize","params":3}`, http.StatusOK, -32602},
		"wrong version":   {`{"jsonrpc":"1.0","id":1,"method":"ping"}`, http.StatusOK, -32600},
		"batch":           {`[{"jsonrpc":"2.0","id":1,"method":"ping"}]`, http.StatusBadRequest, -32600},
		"malformed JSON":  {`{"jsonrpc":`, http.StatusBadRequest, -32700},
		"null request ID": {`{"jsonrpc":"2.0","id":null,"method":"ping"}`, http.StatusOK, -32600},
		"object request ID": {
			`{"jsonrpc":"2.0","id":{},"method":"tools/call","params":{"name":"script"}}`, http.StatusOK, -32600,
		},
		"missing method": {`{"jsonrpc":"2.0","id":1}`, http.StatusOK, -32600},
		"empty object":   {`{}`, http.StatusOK, -32600},
		"call with a result": {
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"script"},"result":{}}`, http.StatusOK, -32600,
		},
		"wrong version notification": {`{"jsonrpc":"1.0","method":"ping"}`, http.StatusOK, -32600},
	} {
		response := mcpRequest(handler, "user-token", tc.body)
		require.Equal(t, tc.status, response.Code, name)
		var body struct {
			Error struct {
				Code float64 `json:"code"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body), name)
		require.InDelta(t, tc.code, body.Error.Code, 0, name)
	}
	stream := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	stream.Header.Set("Authorization", "Bearer user-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, stream)
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
	require.Equal(t, http.MethodPost, w.Header().Get("Allow"))

	clientResponse := mcpRequest(handler, "user-token", `{"jsonrpc":"2.0","id":7,"result":{}}`)
	require.Equal(t, http.StatusAccepted, clientResponse.Code)

	browser := httptest.NewRequest(
		http.MethodPost,
		"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"script"}}`),
	)
	browser.Header.Set("Content-Type", "application/json")
	browser.Header.Set("Authorization", "Bearer user-token")
	browser.Header.Set("Origin", "https://page.example.com")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, browser)
	require.Equal(t, http.StatusForbidden, w.Code, "a browser page must not use the endpoint")
	require.Empty(t, service.calls)

	disabled := api.New(nil, authFunc(allowed), api.Options{})
	require.Equal(
		t,
		http.StatusNotFound,
		mcpRequest(disabled, "user-token", `{"jsonrpc":"2.0","id":1,"method":"ping"}`).Code,
	)
	require.Equal(t, http.StatusNotFound, request(disabled, "GET", "/v1/users/me/external-agent", "", "", "").Code)
}

func TestExternalAgentProfileRoutesRequireConfirmingPermission(t *testing.T) {
	t.Parallel()
	handler := externalHandler(&externalService{}, api.Options{})
	for _, method := range []string{"GET", "PUT"} {
		response := request(handler, method, "/v1/users/me/external-agent", `{"enabled":true}`, "application/json", "")
		require.Equal(t, http.StatusForbidden, response.Code, method)
	}
	capabilities := request(handler, "GET", "/v1/capabilities", "", "", "")
	require.Equal(t, http.StatusOK, capabilities.Code)
	var body struct {
		ExternalAgent struct {
			Resource string   `json:"resource"`
			Access   []string `json:"access"`
			Services bool     `json:"services"`
		} `json:"external_agent"`
	}
	require.NoError(t, json.Unmarshal(capabilities.Body.Bytes(), &body))
	require.Equal(t, "https://loom.example.com/mcp", body.ExternalAgent.Resource)
	require.Equal(t, []string{"history", "memory"}, body.ExternalAgent.Access)
	require.True(t, body.ExternalAgent.Services)
}
