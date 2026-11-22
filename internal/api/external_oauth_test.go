package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/api"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/identity"
	"github.com/complynx/loom/internal/mcpoauth"
)

const prefix = "/oauth/mcp"

func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func TestBrokerRequiresTelegramDecisionAndBindsCode(t *testing.T) {
	t.Parallel()
	const resource = "https://loom.example/mcp"
	const callback = "http://127.0.0.1:52817/callback"
	const downstreamState = "frontend-state"
	const clientName = "Local MCP"
	const upstreamToken = "issuer-token"
	const confirmer = "zinusya-service"
	var upstreamCalls atomic.Int32
	var upstreamChallenge atomic.Value
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		id, secret, _ := r.BasicAuth()
		if id != "broker" || secret != "secret" || r.URL.Path != "/oauth/v2/token" ||
			r.ParseForm() != nil || r.PostForm.Get("redirect_uri") != "https://loom.example/oauth/mcp/callback" ||
			challenge(r.PostForm.Get("code_verifier")) != upstreamChallenge.Load() ||
			r.PostForm.Get("code") != "issuer-code" {
			http.Error(w, "bad exchange", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": upstreamToken, "expires_in": 300, "token_type": "Bearer",
		})
	}))
	defer upstream.Close()
	owner := mcpoauth.Owner{Issuer: upstream.URL, Subject: "owner", Org: "org"}
	var notice mcpoauth.Approval
	var notifyUnavailable bool
	b, err := mcpoauth.New(mcpoauth.Options{
		Resource: resource, UpstreamIssuer: upstream.URL, ClientID: "broker", ClientSecret: "secret",
		Key: make([]byte, 32), HTTP: upstream.Client(), Scopes: []string{"openid", "org-scope"},
		Identify: func(_ context.Context, token string) (mcpoauth.Owner, error) {
			require.Equal(t, upstreamToken, token)
			return owner, nil
		},
		Confirmer: func() string { return confirmer },
		Notify: func(_ context.Context, actor string, approval mcpoauth.Approval) error {
			require.Equal(t, confirmer, actor)
			notice = approval
			if notifyUnavailable {
				return errors.New("fixture Telegram delivery failed")
			}
			return nil
		},
	})
	require.NoError(t, err)
	handler := brokerTestHandler(b, resource, owner, confirmer)
	send := func(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, "https://loom.example"+path, strings.NewReader(body))
		if cookie != nil {
			request.AddCookie(cookie)
		}
		if path == prefix+"/token" {
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	decide := func(caller, id, choice string) int {
		path := "/v1/users/me/external-agent/connections/" + id + "/decision"
		raw, marshalErr := json.Marshal(map[string]string{"choice": choice})
		require.NoError(t, marshalErr)
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+caller)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		return response.Code
	}
	protected := send(http.MethodGet, "/.well-known/oauth-protected-resource/mcp", "", nil)
	require.Equal(t, http.StatusOK, protected.Code)
	require.JSONEq(t, `{"resource":"https://loom.example/mcp","authorization_servers":["https://loom.example"],
		"bearer_methods_supported":["header"],"scopes_supported":["mcp"]}`, protected.Body.String())
	metadata := send(http.MethodGet, "/.well-known/oauth-authorization-server", "", nil)
	require.Contains(t, metadata.Body.String(), `"authorization_response_iss_parameter_supported":true`)
	bad := send(http.MethodPost, prefix+"/register", `{"redirect_uris":["https://other.example/callback"]}`, nil)
	require.Equal(t, http.StatusBadRequest, bad.Code)
	registration := send(
		http.MethodPost,
		prefix+"/register",
		`{"client_name":"`+clientName+`","redirect_uris":["http://127.0.0.1/callback","http://localhost:52811/callback"]}`,
		nil,
	)
	require.Equal(t, http.StatusCreated, registration.Code)
	var registered struct {
		ID string `json:"client_id"`
	}
	require.NoError(t, json.Unmarshal(registration.Body.Bytes(), &registered))
	require.NotEmpty(t, registered.ID)
	verifier := strings.Repeat("A", 43)
	auth := url.Values{
		"client_id": {registered.ID}, "redirect_uri": {callback}, "response_type": {"code"},
		"state": {downstreamState}, "code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"},
		"resource": {resource}, "scope": {"mcp"},
	}
	auth.Set("redirect_uri", "http://127.0.0.1:52817/other")
	require.Equal(t, http.StatusBadRequest, send(http.MethodGet, prefix+"/authorize?"+auth.Encode(), "", nil).Code)
	auth.Set("redirect_uri", callback)
	// Abandoned pre-login requests cannot fill the pool of authenticated flows.
	for range 260 {
		abandoned := send(http.MethodGet, prefix+"/authorize?"+auth.Encode(), "", nil)
		require.Equal(t, http.StatusSeeOther, abandoned.Code)
	}
	start := send(http.MethodGet, prefix+"/authorize?"+auth.Encode(), "", nil)
	require.Equal(t, http.StatusSeeOther, start.Code)
	location, err := url.Parse(start.Header().Get("Location"))
	require.NoError(t, err)
	upstreamState := location.Query().Get("state")
	require.NotEqual(t, downstreamState, upstreamState)
	require.NotEqual(t, challenge(verifier), location.Query().Get("code_challenge"))
	upstreamChallenge.Store(location.Query().Get("code_challenge"))
	require.Equal(t, "openid org-scope", location.Query().Get("scope"))
	cookies := start.Result().Cookies()
	require.Len(t, cookies, 1)
	require.True(t, cookies[0].HttpOnly && cookies[0].Secure)
	path := prefix + "/callback?state=" + upstreamState + "&code=issuer-code"
	require.Equal(t, http.StatusBadRequest, send(http.MethodGet, path, "", nil).Code)
	back := send(http.MethodGet, path, "", cookies[0])
	require.Equal(t, http.StatusSeeOther, back.Code)
	require.EqualValues(t, 1, upstreamCalls.Load())
	require.Equal(t, clientName, notice.Client)
	require.Equal(t, callback, notice.Redirect)
	statusURI := back.Header().Get("Location")
	pending := send(http.MethodGet, statusURI, "", cookies[0])
	require.Equal(t, http.StatusOK, pending.Code)
	require.Empty(t, pending.Header().Get("Location"))
	require.Equal(t, http.StatusForbidden, decide("direct", notice.ID, "approve"))
	require.Equal(t, http.StatusConflict, decide("wrong-actor", notice.ID, "approve"))
	require.Equal(t, http.StatusConflict, decide("other-owner", notice.ID, "approve"))
	require.Equal(t, http.StatusOK, decide("owner", notice.ID, "approve"))
	require.Equal(t, http.StatusConflict, decide("owner", notice.ID, "reject"))
	approved := send(http.MethodGet, statusURI, "", cookies[0])
	require.Equal(t, http.StatusSeeOther, approved.Code)
	returned, err := url.Parse(approved.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, callback, returned.Scheme+"://"+returned.Host+returned.Path)
	require.Equal(t, "https://loom.example", returned.Query().Get("iss"))
	require.Equal(t, downstreamState, returned.Query().Get("state"))
	require.Equal(t, http.StatusOK, decide("owner", notice.ID, "approve"))
	reopened := send(http.MethodGet, path, "", cookies[0])
	require.Equal(t, statusURI, reopened.Header().Get("Location"))
	require.Equal(t, http.StatusBadRequest, send(http.MethodGet, statusURI, "", cookies[0]).Code)
	require.EqualValues(t, 1, upstreamCalls.Load())
	form := url.Values{
		"grant_type": {"authorization_code"}, "client_id": {registered.ID}, "redirect_uri": {callback},
		"code": {returned.Query().Get("code")}, "code_verifier": {strings.Repeat("B", 43)}, "resource": {resource},
	}
	require.Equal(t, http.StatusBadRequest, send(http.MethodPost, prefix+"/token", form.Encode(), nil).Code)
	form.Set("code_verifier", verifier)
	form.Set("redirect_uri", "http://127.0.0.1:52818/callback")
	require.Equal(t, http.StatusBadRequest, send(http.MethodPost, prefix+"/token", form.Encode(), nil).Code)
	form.Set("redirect_uri", callback)
	response := send(http.MethodPost, prefix+"/token", form.Encode(), nil)
	require.Equal(t, http.StatusOK, response.Code)
	var token struct {
		Access string `json:"access_token"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &token))
	got, client, ok := b.Unwrap(token.Access)
	require.True(t, ok)
	require.Equal(t, upstreamToken, got)
	require.Contains(t, client, "mcp:")
	require.NotContains(t, response.Body.String(), upstreamToken)
	const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize",` +
		`"params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"qa","version":"1"}}}`
	require.Equal(t, http.StatusUnauthorized, mcpRequest(handler, upstreamToken, initialize).Code)
	connected := mcpRequest(handler, token.Access, initialize)
	require.Equal(t, http.StatusOK, connected.Code, connected.Body.String())
	require.Equal(t, testMCPSession, connected.Header().Get("Mcp-Session-Id"))
	require.Equal(t, http.StatusBadRequest, send(http.MethodPost, prefix+"/token", form.Encode(), nil).Code)

	start = send(http.MethodGet, prefix+"/authorize?"+auth.Encode(), "", nil)
	location, err = url.Parse(start.Header().Get("Location"))
	require.NoError(t, err)
	upstreamChallenge.Store(location.Query().Get("code_challenge"))
	cookie := start.Result().Cookies()[0]
	back = send(http.MethodGet, prefix+"/callback?state="+location.Query().Get("state")+"&code=issuer-code", "", cookie)
	require.Equal(t, http.StatusSeeOther, back.Code)
	require.Equal(t, http.StatusOK, decide("owner", notice.ID, "reject"))
	require.Equal(t, http.StatusConflict, decide("owner", notice.ID, "approve"))
	rejected := send(http.MethodGet, back.Header().Get("Location"), "", cookie)
	returned, err = url.Parse(rejected.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "access_denied", returned.Query().Get("error"))
	require.Empty(t, returned.Query().Get("code"))
	require.Equal(t, downstreamState, returned.Query().Get("state"))

	notifyUnavailable = true
	start = send(http.MethodGet, prefix+"/authorize?"+auth.Encode(), "", nil)
	location, err = url.Parse(start.Header().Get("Location"))
	require.NoError(t, err)
	upstreamChallenge.Store(location.Query().Get("code_challenge"))
	path = prefix + "/callback?state=" + location.Query().Get("state") + "&code=issuer-code"
	undeliverable := send(http.MethodGet, path, "", start.Result().Cookies()[0])
	returned, err = url.Parse(undeliverable.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, callback, returned.Scheme+"://"+returned.Host+returned.Path)
	require.Equal(t, "access_denied", returned.Query().Get("error"))
	require.Equal(t, http.StatusConflict, decide("owner", notice.ID, "approve"))
}

func brokerTestHandler(b *mcpoauth.Broker, resource string, owner mcpoauth.Owner, confirmer string) http.Handler {
	return api.New(nil, authFunc(func(_ context.Context, r *http.Request) (domain.Principal, error) {
		p := domain.Principal{
			Issuer: owner.Issuer, Subject: owner.Subject, OrgID: owner.Org, Actor: confirmer,
			Permissions: map[string]bool{domain.ExternalAgentProfilePermission: true},
		}
		switch r.Header.Get("Authorization") {
		case "Bearer owner":
		case "Bearer other-owner":
			p.Subject = "other"
		case "Bearer wrong-actor":
			p.Actor = "other-actor"
		case "Bearer direct":
			p.Actor = ""
		default:
			return domain.Principal{}, identity.ErrUnauthorized
		}
		return p, nil
	}), api.Options{ExternalAgent: &api.ExternalAgentOptions{
		Service: &externalService{}, Broker: b, Resource: resource, Issuer: b.Issuer(), Scopes: []string{b.Scope()},
		Authenticate: func(_ context.Context, r *http.Request) (domain.ExternalCaller, error) {
			value := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			_, clientID, ok := b.Unwrap(value)
			if !ok {
				return domain.ExternalCaller{}, identity.ErrUnauthorized
			}
			return domain.ExternalCaller{
				Principal: domain.Principal{Subject: owner.Subject, OrgID: owner.Org, Issuer: owner.Issuer},
				ClientID:  clientID, Token: value,
			}, nil
		},
	}})
}
