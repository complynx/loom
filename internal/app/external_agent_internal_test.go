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
	"github.com/complynx/loom/internal/identity"
	"github.com/complynx/loom/internal/modules"
	"github.com/complynx/loom/internal/sandbox"
)

func TestExternalAgentReadsRecordHistoryWithoutRunOrCredits(t *testing.T) {
	t.Parallel()
	repository, pool := searchToolStore(t)
	ctx := t.Context()
	service := &externalFixtureService{t: t}
	reads, afterRead, held := &service.reads, &service.afterRead, &service.held
	server := httptest.NewServer(service)
	t.Cleanup(server.Close)
	address, err := url.Parse(server.URL)
	require.NoError(t, err)
	registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		return []modules.Endpoint{
			{ContainerID: "service", URI: "http://registered:" + address.Port() + "/manifest", Address: "127.0.0.1"},
		}, nil
	}), modules.Options{})
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(ctx))
	user := domain.Principal{
		Issuer: "https://issuer.example", Subject: "alice", Actor: "alice", OrgID: "org",
		Permissions: map[string]bool{"business.access": true}, ExpiresAt: time.Now().Add(time.Hour),
	}
	caller := domain.ExternalCaller{Principal: user, ClientID: "claude-desktop", Token: "user-token"}
	var tokenRevoked atomic.Bool
	verify := func(context.Context, string) (domain.ExternalCaller, error) {
		if tokenRevoked.Load() {
			return domain.ExternalCaller{}, identity.ErrUnauthorized
		}
		return caller, nil
	}
	tools := runtimeTools{
		repository: repository, registry: registry, executor: sandbox.Inline{},
		cfg: config.Config{
			Sandbox:        config.Sandbox{ScriptTimeoutCeilingMS: 5000},
			ExternalAgents: config.ExternalAgents{CallsPerMinute: 600, ForwardToken: true},
		},
	}
	agent := newExternalAgent(tools, verify)
	script := func(code string) (string, bool) {
		arguments, marshalErr := json.Marshal(map[string]string{"code": code})
		require.NoError(t, marshalErr)
		result, failed := agent.Call(ctx, caller, externalScriptTool, arguments)
		return string(result), failed
	}

	result, failed := script(`return await modules.business.read({})`)
	require.True(t, failed)
	require.Contains(t, result, "external_agent_disabled")
	offered, err := agent.Tools(ctx, caller)
	require.NoError(t, err)
	require.Equal(t, externalDisabledDescription, offered[0].Description)

	tools.cfg.ExternalAgents.CallsPerMinute = 1
	limited := newExternalAgent(tools, verify)
	denied, _ := limited.Call(ctx, caller, externalScriptTool, json.RawMessage(`{"code":"return 1"}`))
	require.Contains(t, string(denied), "external_agent_disabled")
	denied, _ = limited.Call(ctx, caller, externalScriptTool, json.RawMessage(`{"code":"return 1"}`))
	require.Contains(t, string(denied), "rate_limited", "the limit must also bound a caller without access")

	confirming := user
	confirming.Actor = "chat-client"
	confirming.Permissions = map[string]bool{domain.ExternalAgentProfilePermission: true}
	_, err = repository.PutExternalAgentProfile(ctx, user, domain.ExternalAgentProfileUpdate{Enabled: true})
	require.Error(t, err, "the user's own rights must not enable the profile")
	direct := user
	direct.Permissions = map[string]bool{domain.ExternalAgentProfilePermission: true}
	_, err = repository.PutExternalAgentProfile(ctx, direct, domain.ExternalAgentProfileUpdate{Enabled: true})
	require.Error(t, err, "a direct token must not write the profile, also with the permission")
	profile, err := repository.PutExternalAgentProfile(
		ctx,
		confirming,
		domain.ExternalAgentProfileUpdate{Enabled: true, History: true},
	)
	require.NoError(t, err)
	require.EqualValues(t, 1, profile.Version)
	require.Equal(t, "chat-client", profile.ConfirmedBy)
	_, err = repository.PutExternalAgentProfile(ctx, confirming, domain.ExternalAgentProfileUpdate{Enabled: true})
	require.ErrorContains(t, err, "external_agent_profile_version_conflict")
	_, err = pool.Exec(ctx, "UPDATE billing_accounts SET balance=0")
	require.NoError(t, err)

	offered, err = agent.Tools(ctx, caller)
	require.NoError(t, err)
	require.Contains(t, offered[0].Description, `"id":"business"`)
	require.Contains(t, offered[0].Description, `"id":"history"`)
	require.NotContains(t, offered[0].Description, `"id":"memory"`)

	tools.cfg.ExternalAgents = config.ExternalAgents{CallsPerMinute: 600}
	closed := newExternalAgent(tools, verify)
	withoutServices, closedFailed := closed.Call(
		ctx,
		caller,
		externalScriptTool,
		json.RawMessage(`{"code":"return [typeof modules.business, typeof modules.history];"}`),
	)
	require.False(t, closedFailed, string(withoutServices))
	require.JSONEq(t, `["undefined","object"]`, string(withoutServices), "service methods need the operator switch")

	result, failed = script(`const [first] = await Promise.all([modules.business.read({}), modules.business.read({})]);
		return {first, earlier: (await modules.history.read({after_seq: 0})).events.length};`)
	require.False(t, failed, result)
	require.JSONEq(t, `{"first":{"ok":true},"earlier":0}`, result)
	result, failed = script(`return [typeof modules.business.secret, typeof modules.memory, typeof modules.files];`)
	require.False(t, failed, result)
	require.JSONEq(t, `["undefined","undefined","undefined"]`, result)
	result, failed = script(`return (await modules.help.list({module: "business"})).items.map(item => item.id);`)
	require.False(t, failed, result)
	require.JSONEq(t, `["business.read","business.ready"]`, result)
	result, failed = script(`return (await modules.help.read({module: "business", method: "secret"}));`)
	require.True(t, failed)
	require.Contains(t, result, "not_found")

	chat, err := repository.DefaultChat(ctx, user)
	require.NoError(t, err)
	rows, err := pool.Query(
		ctx,
		"SELECT kind,source_actor,text,action FROM chat_events WHERE chat_id=$1 ORDER BY seq",
		chat.ID,
	)
	require.NoError(t, err)
	events := [][4]string{}
	for rows.Next() {
		var event [4]string
		require.NoError(t, rows.Scan(&event[0], &event[1], &event[2], &event[3]))
		events = append(events, event)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, [][4]string{
		{
			domain.ExternalAgentActionEvent,
			"external_agent:claude-desktop",
			"External agent claude-desktop ran a read script. Calls: business.read x2, history.read x1. Outcome: succeeded.",
			externalScriptTool,
		},
	}, events)
	var runs int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM runs").Scan(&runs))
	require.Zero(t, runs, "an external action must not create a run")

	profile, err = repository.PutExternalAgentProfile(
		ctx,
		confirming,
		domain.ExternalAgentProfileUpdate{Enabled: true, History: true, Memory: true, Version: profile.Version},
	)
	require.NoError(t, err)
	result, failed = script(
		`return {revision: typeof (await modules.memory.state({})).revision, put: typeof modules.memory.put};`,
	)
	require.False(t, failed, result)
	require.JSONEq(t, `{"revision":"number","put":"undefined"}`, result)

	agent.scripts <- struct{}{}
	result, failed = script(`return await modules.business.read({})`)
	require.True(t, failed)
	require.Contains(t, result, `"error":"busy"`)
	<-agent.scripts

	_, err = pool.Exec(ctx, `CREATE FUNCTION refuse_external_action() RETURNS trigger AS $$
		BEGIN RAISE EXCEPTION 'history is unavailable'; END $$ LANGUAGE plpgsql`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `CREATE TRIGGER refuse_external_action BEFORE INSERT ON chat_events FOR EACH ROW
		WHEN (NEW.kind = 'external_agent.action') EXECUTE FUNCTION refuse_external_action()`)
	require.NoError(t, err)
	result, failed = script(`return await modules.business.read({})`)
	require.True(t, failed, "a result without a history record must be withheld")
	require.Contains(t, result, "history_unavailable")
	require.NotContains(t, result, `"ok"`)
	_, err = pool.Exec(ctx, "DROP TRIGGER refuse_external_action ON chat_events")
	require.NoError(t, err)
	require.EqualValues(t, 3, reads.Load())

	hold := &heldRead{started: make(chan struct{}), release: make(chan struct{})}
	held.Store(hold)
	result, failed = script(`modules.business.read({}); await modules.business.ready({}); return "done";`)
	close(hold.release)
	held.Store(nil)
	require.False(t, failed, result)
	var unawaited string
	require.NoError(
		t,
		pool.QueryRow(ctx, "SELECT text FROM chat_events WHERE chat_id=$1 ORDER BY seq DESC LIMIT 1", chat.ID).
			Scan(&unawaited),
	)
	require.Equal(
		t,
		"External agent claude-desktop ran a read script. Calls: business.read x1 (1 unfinished), business.ready x1. Outcome: succeeded.",
		unawaited,
		"a call that the script did not wait for must enter the record",
	)
	require.EqualValues(t, 4, reads.Load())

	const twoReads = `const first = await modules.business.read({});
		try { await modules.business.read({}); return "second call was allowed"; }
		catch (failure) { return {first, error: failure.error}; }`
	revokeToken := func() { tokenRevoked.Store(true) }
	afterRead.Store(&revokeToken)
	result, failed = script(twoReads)
	tokenRevoked.Store(false)
	require.False(t, failed, result)
	require.JSONEq(t, `{"first":{"ok":true},"error":"permission_denied"}`, result)
	require.EqualValues(t, 5, reads.Load(), "a revoked token must block the next call of a running script")

	revoke := func() {
		_, revokeErr := repository.PutExternalAgentProfile(
			ctx,
			confirming,
			domain.ExternalAgentProfileUpdate{Version: profile.Version},
		)
		assert.NoError(t, revokeErr)
	}
	afterRead.Store(&revoke)
	result, failed = script(twoReads)
	require.False(t, failed, result)
	require.JSONEq(t, `{"first":{"ok":true},"error":"permission_denied"}`, result)
	require.EqualValues(t, 6, reads.Load(), "a revoked profile must block the next call of a running script")
}

func TestExternalAgentLimiterBoundsOneUserPerMinute(t *testing.T) {
	t.Parallel()
	limiter := &externalLimiter{limit: 2, windows: map[string]externalWindow{}}
	now := time.Now()
	require.True(t, limiter.allow("alice", now))
	require.True(t, limiter.allow("alice", now.Add(time.Second)))
	require.False(t, limiter.allow("alice", now.Add(2*time.Second)))
	require.True(t, limiter.allow("bob", now.Add(2*time.Second)))
	require.True(t, limiter.allow("alice", now.Add(time.Minute)))
}

// heldRead keeps one service read open until the test releases it.
type heldRead struct{ started, release chan struct{} }

// externalFixtureService is a business service with marked and unmarked reads.
// afterRead runs once inside the next read. held keeps the next read open and
// lets /ready return only after that read arrived.
type externalFixtureService struct {
	t         *testing.T
	reads     atomic.Int32
	afterRead atomic.Pointer[func()]
	held      atomic.Pointer[heldRead]
}

func (s *externalFixtureService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/manifest" {
		_ = json.NewEncoder(w).Encode(externalAgentManifest())
		return
	}
	if r.URL.Path != "/read" && r.URL.Path != "/ready" {
		s.t.Errorf("unexpected service call %s", r.URL.Path)
		http.Error(w, "unexpected", http.StatusForbidden)
		return
	}
	assert.Equal(s.t, "Bearer user-token", r.Header.Get("Authorization"))
	var input struct {
		OperationID string `json:"operation_id"`
	}
	if !assert.NoError(s.t, json.NewDecoder(r.Body).Decode(&input)) {
		return
	}
	hold := s.held.Load()
	if r.URL.Path == "/read" {
		s.read(r, hold)
	} else if hold != nil {
		<-hold.started
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"operation_id": input.OperationID, "status": "succeeded", "result": map[string]bool{"ok": true},
	})
}

func (s *externalFixtureService) read(r *http.Request, hold *heldRead) {
	s.reads.Add(1)
	if hook := s.afterRead.Swap(nil); hook != nil {
		(*hook)()
	}
	if hold == nil {
		return
	}
	close(hold.started)
	select {
	case <-hold.release:
	case <-r.Context().Done():
	}
}

func externalAgentManifest() modules.Manifest {
	method := func(id string, external bool) modules.Method {
		return modules.Method{
			ID:             id,
			Description:    "Read fixture data",
			Permissions:    []string{"business.access"},
			Path:           "/" + id,
			Effect:         "read",
			TimeoutMS:      3000,
			MaxResultBytes: 4096,
			InputSchema:    json.RawMessage(`{"type":"object","additionalProperties":false}`),
			OutputSchema: json.RawMessage(
				`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`,
			),
			Confirmation:      modules.Confirmation{Mode: "none"},
			ArtifactMIMETypes: []string{},
			ExternalAgent:     external,
		}
	}
	return modules.Manifest{
		ProtocolVersion: 1,
		ServiceID:       "external-fixture",
		Revision:        "v1",
		Description:     "External agent fixture",
		Modules: []modules.Module{{
			ID:          "business",
			Description: "Business reads",
			Permissions: []string{"business.access"},
			Discovery:   "static",
			Methods:     []modules.Method{method("read", true), method("ready", true), method("secret", false)},
		}},
	}
}
