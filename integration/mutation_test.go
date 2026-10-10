package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/complynx/clx-scripter/internal/app"
	"github.com/complynx/clx-scripter/internal/domain"
	"github.com/complynx/clx-scripter/internal/modules"
	"github.com/complynx/clx-scripter/internal/operations"
	"github.com/complynx/clx-scripter/internal/provider"
	"github.com/complynx/clx-scripter/internal/runs"
	"github.com/stretchr/testify/require"
)

type bookingService struct {
	mu       sync.Mutex
	prepared []string
	executed []string
	status   []string
	server   *httptest.Server
}

func newBookingService(t *testing.T, lostReply bool) (*bookingService, *modules.Registry) {
	t.Helper()
	raw, err := os.ReadFile("../examples/manifest.json")
	require.NoError(t, err)
	var manifest modules.Manifest
	require.NoError(t, json.Unmarshal(raw, &manifest))
	manifest.Modules = manifest.Modules[:1]
	manifest.Modules[0].Methods = manifest.Modules[0].Methods[1:]
	manifest.Modules[0].Methods[0].TimeoutMS = 100
	service := &bookingService{}
	service.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manifest" {
			_ = json.NewEncoder(w).Encode(manifest)
			return
		}
		if r.Header.Get("Authorization") != "Bearer delegated-user-token" {
			w.WriteHeader(401)
			return
		}
		var envelope struct {
			OperationID    string          `json:"operation_id"`
			Arguments      json.RawMessage `json:"arguments"`
			IdempotencyKey string          `json:"idempotency_key"`
			Confirmation   map[string]any  `json:"confirmation"`
		}
		if json.NewDecoder(r.Body).Decode(&envelope) != nil {
			w.WriteHeader(400)
			return
		}
		switch r.URL.Path {
		case "/internal/agent/massage/prepare-booking":
			service.mu.Lock()
			service.prepared = append(service.prepared, envelope.OperationID)
			service.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "prepared", "operation_id": envelope.OperationID, "confirmation_id": "prepared-slot-confirmation", "arguments": json.RawMessage(`{"slot_id":"slot-15"}`), "expires_at": time.Now().Add(time.Minute), "description": "Book Andrey at 15:00"})
		case "/internal/agent/massage/book-slot":
			if envelope.OperationID != envelope.IdempotencyKey || envelope.Confirmation["confirmation_id"] != "prepared-slot-confirmation" {
				w.WriteHeader(422)
				return
			}
			service.mu.Lock()
			service.executed = append(service.executed, envelope.OperationID)
			service.mu.Unlock()
			if lostReply {
				select {
				case <-r.Context().Done():
				case <-time.After(time.Second):
				}
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "succeeded", "operation_id": envelope.OperationID, "result": map[string]string{"booking_id": "booking-15"}})
		case "/internal/agent/operations/status":
			service.mu.Lock()
			service.status = append(service.status, envelope.OperationID)
			service.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "succeeded", "operation_id": envelope.OperationID, "result": map[string]string{"booking_id": "booking-15"}})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(service.server.Close)
	parsed, err := url.Parse(service.server.URL)
	require.NoError(t, err)
	registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		return []modules.Endpoint{{ContainerID: "qa-trusted-service", URI: "http://registered-service:" + parsed.Port() + "/manifest", Address: "127.0.0.1"}}, nil
	}), modules.Options{})
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(context.Background()))
	return service, registry
}
func mutationWorker(t *testing.T, f *fixture, registry *modules.Registry) (*modelFixture, *runs.Engine) {
	t.Helper()
	model, _ := fakeModel(t, f, func(index int) any {
		if index == 1 {
			return map[string]any{"id": "provider-write", "status": "completed", "output": []any{map[string]any{"type": "function_call", "call_id": "book-request-stable", "name": "module_call", "arguments": `{"module":"massage","method":"book_slot","arguments":{"slot_id":"slot-15"}}`}}, "usage": map[string]int{"input_tokens": 10, "output_tokens": 5}}
		}
		return finalResponse("Booked Andrey at 15:00, booking-15")
	})
	authorize := func(context.Context, domain.Run) (domain.Principal, string, error) {
		return domain.Principal{Issuer: "https://test.identity", Subject: "alice", Actor: "alice", OrgID: "test-org", Permissions: map[string]bool{"runs.create": true, "chat.read": true, "zns.massage.access": true, "zns.massage.book": true}}, "delegated-user-token", nil
	}
	bridge := app.NewModuleBridge(registry, &operations.Coordinator{Pool: f.pool}, f.repository, authorize)
	f.repository.SetApprovalHandler(bridge.ApproveTx)
	client, err := provider.NewResponses(model.server.URL, "test-provider-key", model.server.Client())
	require.NoError(t, err)
	engine := runs.New(f.repository, client, runs.Options{Owner: "mutation-worker", Authorize: authorize, ModuleCall: bridge.Call, Tools: func(context.Context, domain.Run, domain.Principal, string) ([]provider.Tool, error) {
		return []provider.Tool{{Type: "function", Name: "module_call", Description: "Execute approved registered service action", Parameters: json.RawMessage(`{"type":"object"}`)}}, nil
	}})
	return model, engine
}
func TestHTTPMutationRequiresApprovalAndExecutesStableOperationOnce(t *testing.T) {
	f := newFixture(t)
	service, registry := newBookingService(t, false)
	model, engine := mutationWorker(t, f, registry)
	request := runRequest("", "", "Book Andrey at 15:00")
	request.Interaction.Approvals = true
	run := f.createRun(t, request, "mutation-run")
	require.NoError(t, engine.Execute(context.Background(), run.ID))
	waiting := f.run(t, run.ID)
	require.Equal(t, domain.WaitingApproval, waiting.State)
	require.NotNil(t, waiting.Approval)
	require.Equal(t, "Book Andrey at 15:00", waiting.Approval.Text)
	service.mu.Lock()
	require.Len(t, service.prepared, 1)
	require.Empty(t, service.executed)
	preparedID := service.prepared[0]
	service.mu.Unlock()
	require.Equal(t, preparedID, waiting.Approval.OperationID)
	// Passive UI activity that contains approval words never grants consent.
	event := domain.AppendEvent{SourceEventID: "passive-approve", Type: "user.action", Action: "button.selected", Text: "approve booking-15", OccurredAt: time.Now()}
	code, raw := f.request(t, "alice", "POST", "/v1/chats/"+run.ChatID+"/events", "", event)
	require.Equal(t, 201, code, string(raw))
	require.Equal(t, domain.WaitingApproval, f.run(t, run.ID).State)
	code, raw = f.request(t, "bob", "POST", "/v1/runs/"+run.ID+"/input", "bad-approval", domain.RunInput{Kind: "approval", ApprovalID: waiting.Approval.ID, Decision: "approve"})
	require.Equal(t, 404, code, string(raw))
	code, raw = f.request(t, "alice", "GET", "/v1/runs/"+run.ID+"/events", "", nil)
	require.Equal(t, 200, code, string(raw))
	require.Contains(t, string(raw), "approval")
	answer := domain.RunInput{Kind: "approval", ApprovalID: waiting.Approval.ID, Decision: "approve"}
	code, raw = f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/input", "approve-booking", answer)
	require.Equal(t, 200, code, string(raw))
	code, raw = f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/input", "approve-booking", answer)
	require.Equal(t, 200, code, string(raw))
	require.NoError(t, engine.Execute(context.Background(), run.ID))
	final := f.run(t, run.ID)
	require.Equal(t, domain.Completed, final.State)
	require.Contains(t, final.Result, "booking-15")
	service.mu.Lock()
	require.Equal(t, []string{preparedID}, service.executed)
	require.Empty(t, service.status)
	service.mu.Unlock()
	model.mu.Lock()
	require.Len(t, model.requests, 2)
	require.Contains(t, string(model.requests[1]), "booking-15")
	model.mu.Unlock()
}
func TestUnknownMutationRecoversWithStatusAndNeverRepeatsEffect(t *testing.T) {
	f := newFixture(t)
	service, registry := newBookingService(t, true)
	_, engine := mutationWorker(t, f, registry)
	request := runRequest("", "", "Book requested slot")
	request.Interaction.Approvals = true
	run := f.createRun(t, request, "unknown-write")
	require.NoError(t, engine.Execute(context.Background(), run.ID))
	waiting := f.run(t, run.ID)
	require.Equal(t, domain.WaitingApproval, waiting.State)
	code, raw := f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/input", "approve", domain.RunInput{Kind: "approval", ApprovalID: waiting.Approval.ID, Decision: "approve"})
	require.Equal(t, 200, code, string(raw))
	require.NoError(t, engine.Execute(context.Background(), run.ID))
	unknown := f.run(t, run.ID)
	require.Equal(t, domain.Reconciling, unknown.State)
	code, raw = f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/resume", "recover-status", nil)
	require.Equal(t, 200, code, string(raw))
	require.NoError(t, engine.Execute(context.Background(), run.ID))
	require.Equal(t, domain.Completed, f.run(t, run.ID).State)
	service.mu.Lock()
	require.Len(t, service.prepared, 1)
	require.Equal(t, service.prepared, service.executed)
	require.Equal(t, service.prepared, service.status)
	service.mu.Unlock()
}
