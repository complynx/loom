package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/identity"
	"github.com/complynx/loom/internal/modules"
	"github.com/complynx/loom/internal/operations"
)

const externalWriteTestManifest = `{
	"protocol_version":1,"service_id":"chat","revision":"fixture","description":"Chat service.",
	"caller":{"permissions":["external_agent.profile.write"],
		"auth":{"type":"zitadel","issuer":"https://issuer.example","subject":"frontend","audience":"api"}},
	"external_agent_tokens":{"path":"/token","approval_path":"/notification"},
	"modules":[{"id":"business","description":"Business.","permissions":[],"discovery":"static","methods":[{
		"id":"save","description":"Save.","permissions":[],"path":"/write","effect":"write","timeout_ms":1000,
		"max_result_bytes":1000,"input_schema":{"type":"object"},"output_schema":{"type":"object"},
		"idempotency":true,"status_path":"/status","deduplication_window_seconds":86400,
		"confirmation":{"mode":"exact_parameters","prepare_path":"/prepare"},
		"artifact_mime_types":[],"external_disclosure":false,"external_agent":true,"external_agent_remember":true
	},{
		"id":"critical","description":"Save with mandatory approval.","permissions":[],"path":"/write",
		"effect":"write","timeout_ms":1000,"max_result_bytes":1000,"input_schema":{"type":"object"},
		"output_schema":{"type":"object"},"idempotency":true,"status_path":"/status",
		"deduplication_window_seconds":86400,"confirmation":{"mode":"exact_parameters","prepare_path":"/prepare"},
		"artifact_mime_types":[],"external_disclosure":false,"external_agent":true
	},{
		"id":"private","description":"Save the supplied number.","permissions":[],"path":"/write-private",
		"effect":"write","timeout_ms":1000,"max_result_bytes":1000,"input_schema":{"type":"object",
			"properties":{"passport_number":{"type":"string"}},"required":["passport_number"]},
		"output_schema":{"type":"object"},"idempotency":true,"status_path":"/status",
		"deduplication_window_seconds":86400,"confirmation":{"mode":"none"},
		"artifact_mime_types":[],"external_disclosure":false,"external_agent":true,
		"external_agent_protected_arguments":true
	}]}]
}`

const syntheticMCPPassport = "SYNTHETIC-MCP-PASSPORT-REPLAY"

type externalWritePeer struct {
	t                *testing.T
	notifications    atomic.Int32
	failNotification atomic.Bool
	writes           atomic.Int32
	changeCritical   atomic.Bool
	completed        sync.Map
	deniedReceipt    atomic.Pointer[string]
}

func (p *externalWritePeer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/manifest":
		document := externalWriteTestManifest
		if p.changeCritical.Load() {
			document = strings.Replace(document, "Save with mandatory approval.", "Review each critical save.", 1)
		}
		_, _ = fmt.Fprint(w, document)
	case "/notification":
		assert.Equal(p.t, "Bearer fixture-secret", r.Header.Get("Authorization"))
		var in domain.ExternalApproval
		assert.NoError(p.t, json.NewDecoder(r.Body).Decode(&in))
		assert.Equal(p.t, "alice", in.Subject)
		assert.Equal(p.t, "Save this record", in.Description)
		assert.JSONEq(p.t, "{}", string(in.Arguments))
		assert.Equal(p.t, in.Method == "save", in.Remember)
		p.notifications.Add(1)
		if p.failNotification.Swap(false) {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = fmt.Fprint(w, "{}")
	case "/prepare", "/write", "/write-private":
		assert.Equal(p.t, "Bearer downstream", r.Header.Get("Authorization"))
		assert.Equal(p.t, "fixture-secret", r.Header.Get("X-Loom-External-Write-Key"))
		var in struct {
			ID        string            `json:"operation_id"`
			Arguments map[string]string `json:"arguments"`
		}
		assert.NoError(p.t, json.NewDecoder(r.Body).Decode(&in))
		if r.URL.Path == "/write-private" {
			assert.Equal(p.t, syntheticMCPPassport, in.Arguments["passport_number"])
		}
		if r.URL.Path == "/prepare" {
			_, _ = fmt.Fprintf(w, `{"status":"prepared","operation_id":%q,"description":"Save this record",`+
				`"arguments":{},"preconditions":{"version":1},"expires_at":%q}`,
				in.ID, time.Now().Add(time.Minute).Format(time.RFC3339Nano))
			return
		}
		p.writes.Add(1)
		p.completed.Store(in.ID, true)
		p.savedReceipt(w, in.ID)
	case "/status":
		p.status(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (p *externalWritePeer) status(w http.ResponseWriter, r *http.Request) {
	assert.Equal(p.t, "Bearer downstream", r.Header.Get("Authorization"))
	assert.Equal(p.t, "fixture-secret", r.Header.Get("X-Loom-External-Write-Key"))
	var in struct {
		ID string `json:"operation_id"`
	}
	assert.NoError(p.t, json.NewDecoder(r.Body).Decode(&in))
	_, completed := p.completed.Load(in.ID)
	if denied := p.deniedReceipt.Load(); !completed || denied != nil && *denied == in.ID {
		_, _ = fmt.Fprintf(w, `{"status":"not_found","operation_id":%q}`, in.ID)
		return
	}
	p.savedReceipt(w, in.ID)
}

func (p *externalWritePeer) savedReceipt(w http.ResponseWriter, id string) {
	_, _ = fmt.Fprintf(w, `{"status":"succeeded","operation_id":%q,"result":{"saved":true}}`, id)
}

func externalWriteTestInput(method, request string) json.RawMessage {
	return json.RawMessage(
		fmt.Sprintf(`{"module":"business","method":%q,"request_id":%q,"arguments":{}}`, method, request),
	)
}

func TestExternalWritesWaitForTrustedDecisionAndRememberOnlyForSession(t *testing.T) {
	t.Parallel()
	repository, pool := searchToolStore(t)
	ctx := t.Context()
	peer := &externalWritePeer{t: t}
	server := httptest.NewServer(peer)
	t.Cleanup(server.Close)
	address, err := url.Parse(server.URL)
	require.NoError(t, err)
	registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		return []modules.Endpoint{{ContainerID: "chat", URI: "http://registered:" + address.Port() + "/manifest",
			Address: "127.0.0.1"}}, nil
	}), modules.Options{})
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(ctx))
	user := domain.Principal{Issuer: "https://issuer.example", Subject: "alice", Actor: "alice", OrgID: "org"}
	caller := domain.ExternalCaller{Principal: user, ClientID: "fixture-client", Token: "external"}
	confirming := user
	confirming.Actor = "frontend"
	confirming.Permissions = map[string]bool{domain.ExternalAgentProfilePermission: true}
	_, err = repository.PutExternalAgentProfile(
		ctx,
		confirming,
		domain.ExternalAgentProfileUpdate{Enabled: true, Writes: true},
	)
	require.NoError(t, err)
	cipher, err := identity.NewSessionCipher("test-key", make([]byte, 32))
	require.NoError(t, err)
	agent := newExternalAgent(runtimeTools{repository: repository, registry: registry, externalArgumentCipher: cipher,
		cfg: config.Config{ExternalAgents: config.ExternalAgents{CallsPerMinute: 600,
			TokenServices: map[string]string{"chat": "fixture-secret"}}}},
		func(context.Context, string) (domain.ExternalCaller, error) { return caller, nil },
		func() []modules.TokenService { return []modules.TokenService{{ServiceID: "chat"}} },
		func(context.Context, domain.ExternalCaller, string) (string, error) { return "downstream", nil })
	caller.SessionID, err = agent.OpenSession(ctx, caller)
	require.NoError(t, err)

	prepare := func(current domain.ExternalCaller, method, request string) (string, string) {
		t.Helper()
		out, failed := agent.Call(ctx, current, externalWrite, externalWriteTestInput(method, request))
		require.False(t, failed, string(out))
		var result struct {
			ID     string `json:"operation_id"`
			Status string `json:"status"`
		}
		require.NoError(t, json.Unmarshal(out, &result))
		return result.ID, result.Status
	}
	firstRequest := uuid.NewString()
	first, status := prepare(caller, "save", firstRequest)
	require.Equal(t, "waiting_approval", status)
	require.EqualValues(t, 1, peer.notifications.Load())
	require.Zero(t, peer.writes.Load())
	coordinator := &operations.Coordinator{Pool: pool}
	_, err = coordinator.DecideExternal(ctx, first, user.Issuer, "bob", user.OrgID, confirming.Actor, "all")
	require.ErrorIs(t, err, operations.ErrBinding)
	_, err = coordinator.DecideExternal(ctx, first, user.Issuer, user.Subject, user.OrgID, confirming.Actor, "all")
	require.NoError(t, err)
	require.Zero(t, peer.writes.Load(), "Telegram approval alone does not dispatch")
	verifyExternalUnsentRecovery(t, coordinator, first)
	_, err = coordinator.DecideExternal(ctx, first, user.Issuer, user.Subject, user.OrgID, confirming.Actor, "once")
	require.ErrorIs(t, err, operations.ErrState)
	replayedApproval, status := prepare(caller, "save", firstRequest)
	require.Equal(t, first, replayedApproval)
	require.Equal(t, "approved", status)
	require.Zero(t, peer.writes.Load(), "lookup by request ID must not dispatch an approved operation")
	args := json.RawMessage(fmt.Sprintf(`{"operation_id":%q}`, first))
	for range 2 {
		result, failed := agent.Call(ctx, caller, externalWriteContinue, args)
		require.False(t, failed, string(result))
		require.Contains(t, string(result), `"succeeded"`)
	}
	_, err = coordinator.DecideExternal(ctx, first, user.Issuer, user.Subject, user.OrgID, confirming.Actor, "all")
	require.NoError(t, err, "an exact Telegram retry remains idempotent after dispatch")
	peer.changeCritical.Store(true)
	require.NoError(t, registry.Refresh(ctx))
	result, failed := agent.Call(ctx, caller, externalWriteStatus, args)
	require.False(t, failed, string(result))
	require.Contains(t, string(result), `"succeeded"`)
	require.Contains(t, string(result), `"saved":true`)
	// A client that lost its first response has only request_id and the original arguments.
	replayed, status := prepare(caller, "save", firstRequest)
	require.Equal(t, first, replayed)
	require.Equal(t, "succeeded", status)
	verifyDifferentExternalCallRejected(t, agent, caller, firstRequest)
	require.EqualValues(t, 1, peer.writes.Load(), "reading the receipt never repeats the write")

	second, status := prepare(caller, "save", uuid.NewString())
	require.Equal(t, "succeeded", status)
	require.EqualValues(t, 2, peer.writes.Load())
	verifyExternalReceiptDenial(t, agent, caller, peer, first, firstRequest, second)
	critical := verifyExternalApprovalDeliveryReplay(t, agent, caller, peer)
	_, err = coordinator.DecideExternal(ctx, critical, user.Issuer, user.Subject, user.OrgID, confirming.Actor, "all")
	require.ErrorIs(t, err, operations.ErrBinding)
	require.EqualValues(t, 2, peer.writes.Load())
	verifyExternalPreparationRecovery(t, coordinator, critical)
	verifyExternalProtectedArguments(t, agent, caller, peer, coordinator)
	fresh := caller
	fresh.SessionID, err = agent.OpenSession(ctx, caller)
	require.NoError(t, err)
	_, status = prepare(fresh, "save", uuid.NewString())
	require.Equal(t, "waiting_approval", status)
	require.NoError(t, agent.CloseSession(ctx, caller))
	var runs, grants int
	require.NoError(
		t,
		pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM runs),(SELECT count(*) FROM external_agent_approval_rules)").
			Scan(&runs, &grants),
	)
	require.Zero(t, runs)
	require.Zero(t, grants)
}

func verifyExternalApprovalDeliveryReplay(
	t *testing.T, agent *ExternalAgent, caller domain.ExternalCaller, peer *externalWritePeer,
) string {
	t.Helper()
	ctx := t.Context()
	input := externalWriteTestInput("critical", uuid.NewString())
	before := peer.notifications.Load()
	peer.failNotification.Store(true)
	lost, failed := agent.Call(ctx, caller, externalWrite, input)
	require.False(t, failed, string(lost))
	require.Contains(t, string(lost), `"approval_delivery":"unconfirmed"`)

	replay, failed := agent.Call(ctx, caller, externalWrite, input)
	require.False(t, failed, string(replay))
	require.Contains(t, string(replay), `"status":"waiting_approval"`)
	require.Contains(t, string(replay), `"approval_delivery":"unconfirmed"`)
	require.Contains(t, string(replay), "call write_continue")
	require.Equal(t, before+1, peer.notifications.Load(), "Request replay only retrieves the receipt")
	var receipt struct {
		ID string `json:"operation_id"`
	}
	require.NoError(t, json.Unmarshal(replay, &receipt))
	require.NotEmpty(t, receipt.ID)
	args := json.RawMessage(fmt.Sprintf(`{"operation_id":%q}`, receipt.ID))
	resumed, failed := agent.Call(ctx, caller, externalWriteContinue, args)
	require.False(t, failed, string(resumed))
	require.Contains(t, string(resumed), `"approval_delivery":"telegram"`)
	require.Equal(t, before+2, peer.notifications.Load())
	return receipt.ID
}

func verifyExternalProtectedArguments(
	t *testing.T, agent *ExternalAgent, caller domain.ExternalCaller, peer *externalWritePeer,
	coordinator *operations.Coordinator,
) {
	t.Helper()
	ctx := t.Context()
	writeCount := peer.writes.Load()
	notificationCount := peer.notifications.Load()
	request := uuid.NewString()
	input := fmt.Sprintf(`{"module":"business","method":"private","request_id":%q,"arguments":{"passport_number":%q}}`,
		request, syntheticMCPPassport)
	var receipt struct {
		ID string `json:"operation_id"`
	}
	for range 2 {
		out, failed := agent.Call(ctx, caller, externalWrite, json.RawMessage(input))
		require.False(t, failed, string(out))
		require.Contains(t, string(out), `"succeeded"`)
		require.NotContains(t, string(out), syntheticMCPPassport)
		require.NoError(t, json.Unmarshal(out, &receipt))
	}
	require.Equal(t, writeCount+1, peer.writes.Load())
	require.Equal(t, notificationCount, peer.notifications.Load())
	var raw string
	require.NoError(t, coordinator.Pool.QueryRow(ctx, "SELECT doc::text FROM write_operations WHERE id=$1", receipt.ID).
		Scan(&raw))
	require.NotContains(t, raw, syntheticMCPPassport)
	require.Contains(t, raw, `"arguments": null`)
	require.NotContains(t, raw, `"normalized_arguments"`)
	require.Contains(t, raw, `"protected_arguments"`)
	require.Contains(t, raw, `"argument_hash": ""`)
	changed := strings.Replace(input, syntheticMCPPassport, "A-DIFFERENT-SYNTHETIC-PASSPORT", 1)
	out, failed := agent.Call(ctx, caller, externalWrite, json.RawMessage(changed))
	require.True(t, failed, string(out))
	require.Equal(t, writeCount+1, peer.writes.Load())
}

func verifyDifferentExternalCallRejected(
	t *testing.T, agent *ExternalAgent, caller domain.ExternalCaller, requestID string,
) {
	t.Helper()
	input := json.RawMessage(fmt.Sprintf(
		`{"module":"business","method":"save","request_id":%q,"arguments":{"changed":true}}`, requestID))
	result, failed := agent.Call(t.Context(), caller, externalWrite, input)
	require.True(t, failed, string(result))
}

func verifyExternalReceiptDenial(
	t *testing.T, agent *ExternalAgent, caller domain.ExternalCaller, peer *externalWritePeer,
	first, firstRequest, second string,
) {
	t.Helper()
	peer.deniedReceipt.Store(&first)
	ctx := t.Context()
	denied := json.RawMessage(fmt.Sprintf(`{"operation_id":%q}`, first))
	for _, tool := range []string{externalWriteStatus, externalWriteContinue} {
		out, failed := agent.Call(ctx, caller, tool, denied)
		require.True(t, failed, string(out))
		require.NotContains(t, string(out), `"saved":true`)
	}
	out, failed := agent.Call(ctx, caller, externalWrite, externalWriteTestInput("save", firstRequest))
	require.True(t, failed, string(out))
	require.NotContains(t, string(out), `"saved":true`)
	stillAllowed := json.RawMessage(fmt.Sprintf(`{"operation_id":%q}`, second))
	out, failed = agent.Call(ctx, caller, externalWriteStatus, stillAllowed)
	require.False(t, failed, string(out))
	require.Contains(t, string(out), `"saved":true`)
}

func verifyExternalUnsentRecovery(t *testing.T, coordinator *operations.Coordinator, operationID string) {
	t.Helper()
	ctx := t.Context()
	before, err := coordinator.Get(ctx, operationID)
	require.NoError(t, err)
	backend := modules.WriteBackend{Guard: func(context.Context) error {
		return context.DeadlineExceeded
	}}
	allow := func(context.Context, operations.Operation) error { return nil }
	_, err = coordinator.DispatchExternal(ctx, before, allow, backend)
	var notDispatched *operations.NotDispatchedError
	require.ErrorAs(t, err, &notDispatched)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	after, err := coordinator.Get(ctx, operationID)
	require.NoError(t, err)
	require.Equal(t, "approved", after.State)
	require.Equal(t, before.BindingHash, after.BindingHash)
}

func verifyExternalPreparationRecovery(t *testing.T, coordinator *operations.Coordinator, operationID string) {
	t.Helper()
	ctx := t.Context()
	op, err := coordinator.Get(ctx, operationID)
	require.NoError(t, err)
	_, err = coordinator.Pool.Exec(ctx, `UPDATE write_operations
		SET state='preparing',doc=jsonb_set(doc,'{state}','"preparing"'),updated_at=clock_timestamp()
		WHERE id=$1`, operationID)
	require.NoError(t, err)

	before, err := coordinator.Prepare(ctx, op.Request, nil)
	require.NoError(t, err)
	require.Equal(t, operationID, before.ID)
	require.Equal(t, "preparing", before.State)

	_, err = coordinator.Pool.Exec(ctx, `UPDATE write_operations
		SET updated_at=clock_timestamp()-interval '6 minutes' WHERE id=$1`, operationID)
	require.NoError(t, err)
	after, err := coordinator.Prepare(ctx, op.Request, nil)
	require.NoError(t, err)
	require.Equal(t, operationID, after.ID)
	require.Equal(t, "failed", after.State)
	require.Equal(t, "preparation_interrupted", after.FailureCode)
}
