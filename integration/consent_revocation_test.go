package integration_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/app"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/operations"
	"github.com/complynx/loom/internal/provider"
	"github.com/complynx/loom/internal/runs"
)

func TestRevokedConsentDoesNotReviveAfterRightsRestoration(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	service, registry := newBookingService(t, false)
	var revoked atomic.Bool
	p := domain.Principal{
		Issuer:  "https://test.identity",
		Subject: "alice",
		Actor:   "alice",
		OrgID:   "test-org",
		Permissions: map[string]bool{
			"runs.create":            true,
			"chat.read":              true,
			"booking.massage.access": true,
			"booking.massage.book":   true,
		},
	}
	authorize := func(context.Context, domain.Run) (domain.Principal, string, error) {
		if revoked.Load() {
			return domain.Principal{}, "", domain.Forbidden()
		}
		return p, "delegated-user-token", nil
	}
	coordinator := &operations.Coordinator{Pool: f.pool}
	bridge := app.NewModuleBridge(registry, coordinator, f.repository, authorize)
	f.repository.SetApprovalHandler(bridge.ApproveTx)
	request := runRequest("", "", "Book Andrey at 15:00")
	request.Interaction.Approvals = true
	run := f.createRun(t, request, "consent-revocation")
	run, err := f.repository.ClaimRun(t.Context(), run.ID, "consent-worker", time.Minute)
	require.NoError(t, err)
	call := provider.Call{
		ID:   "same-call",
		Name: "module_call",
		Arguments: json.RawMessage(
			`{"module":"massage","method":"book_slot","arguments":{"slot_id":"slot-15"},"confirmation_text":"Confirm booking"}`,
		),
	}
	_, approval, err := bridge.Call(t.Context(), run, p, "", call)
	require.NoError(t, err)
	require.NotNil(t, approval)
	approval.Call = &domain.ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments}
	require.NoError(t, f.repository.AskApproval(t.Context(), run.ID, "consent-worker", run.Fence, *approval))
	code, raw := f.request(
		t,
		"alice",
		"POST",
		"/v1/runs/"+run.ID+"/input",
		"first-consent",
		domain.RunInput{Kind: "approval", ApprovalID: approval.ID, Decision: "approve"},
	)
	require.Equal(t, 200, code, string(raw))
	run, err = f.repository.ClaimRun(t.Context(), run.ID, "consent-worker", time.Minute)
	require.NoError(t, err)
	revoked.Store(true)
	other := call
	other.ID = "unbound-call-observes-revocation"
	raw, _, err = bridge.Call(t.Context(), run, p, "", other)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"error":"authority_changed"`)
	stored, err := coordinator.Get(t.Context(), approval.OperationID)
	require.NoError(t, err)
	require.Equal(t, "prepared", stored.State)
	raw, next, err := bridge.Call(t.Context(), run, p, "", call)
	require.NoError(t, err)
	require.NotNil(t, next)
	require.Equal(t, "authority_changed", next.FailureCode)
	require.Contains(t, string(raw), `"error":"authority_changed"`)
	require.Contains(t, string(raw), approval.OperationID)
	fresh := f.run(t, run.ID)
	require.NotEqual(t, approval.ID, fresh.Approval.ID)
	require.Equal(t, approval.OperationID, fresh.Approval.OperationID)
	require.Equal(t, approval.ExpiresAt, fresh.Approval.ExpiresAt)
	require.Empty(t, fresh.Approval.Decision)
	// Rebuild the bridge/coordinator, restore rights, and retry the same call.
	revoked.Store(false)
	bridge = app.NewModuleBridge(registry, &operations.Coordinator{Pool: f.pool}, f.repository, authorize)
	f.repository.SetApprovalHandler(bridge.ApproveTx)
	_, next, err = bridge.Call(t.Context(), fresh, p, "", call)
	require.NoError(t, err)
	require.NotNil(t, next)
	require.Equal(t, fresh.Approval.ID, next.ID)
	require.Equal(t, approval.OperationID, next.OperationID)
	service.mu.Lock()
	require.Len(t, service.prepared, 1)
	require.Empty(t, service.executed)
	service.mu.Unlock()
	require.NoError(t, f.repository.AskApproval(t.Context(), run.ID, "consent-worker", run.Fence, *next))
	code, raw = f.request(
		t,
		"alice",
		"POST",
		"/v1/runs/"+run.ID+"/input",
		"stale-consent",
		domain.RunInput{Kind: "approval", ApprovalID: approval.ID, Decision: "approve"},
	)
	require.Equal(t, 409, code, string(raw))
	code, raw = f.request(
		t,
		"alice",
		"POST",
		"/v1/runs/"+run.ID+"/input",
		"fresh-consent",
		domain.RunInput{Kind: "approval", ApprovalID: next.ID, Decision: "approve"},
	)
	require.Equal(t, 200, code, string(raw))
	run, err = f.repository.ClaimRun(t.Context(), run.ID, "consent-worker", time.Minute)
	require.NoError(t, err)
	raw, next, err = bridge.Call(t.Context(), run, p, "", call)
	require.NoError(t, err)
	require.Nil(t, next)
	require.Contains(t, string(raw), "booking-15")
	service.mu.Lock()
	require.Equal(t, []string{approval.OperationID}, service.executed)
	service.mu.Unlock()
}

func TestEngineReissuesOriginalApprovalAfterRevocation(t *testing.T) {
	t.Parallel()
	for _, replyMode := range []string{domain.ReplyModeProtocol, domain.ReplyModeAgent} {
		t.Run(replyMode, func(t *testing.T) {
			t.Parallel()
			testRevokedEngine(t, replyMode)
		})
	}
}

func TestNoneModeRequiresConsentAfterPredispatchAuthorityLoss(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	service, registry := newBookingServiceConfirmation(t, false, "", "none")
	model, _ := mutationWorker(t, f, registry)
	p := domain.Principal{
		Issuer:  "https://test.identity",
		Subject: "alice",
		Actor:   "alice",
		OrgID:   "test-org",
		Permissions: map[string]bool{
			"runs.create":            true,
			"chat.read":              true,
			"booking.massage.access": true,
			"booking.massage.book":   true,
		},
	}
	var checks atomic.Int32
	var restored atomic.Bool
	var outerAuthorize app.RunAuthorizer = func(context.Context, domain.Run) (domain.Principal, string, error) {
		return p, "delegated-user-token", nil
	}
	bridgeAuthorize := func(ctx context.Context, run domain.Run) (domain.Principal, string, error) {
		if checks.Add(1) >= 4 && !restored.Load() {
			return domain.Principal{}, "", domain.Forbidden()
		}
		return outerAuthorize(ctx, run)
	}
	bridge := app.NewModuleBridge(registry, &operations.Coordinator{Pool: f.pool}, f.repository, bridgeAuthorize)
	f.repository.SetApprovalHandler(bridge.ApproveTx)
	client, err := provider.NewResponses(model.server.URL, "test-provider-key", model.server.Client())
	require.NoError(t, err)
	engine := runs.New(f.repository, client, runs.Options{
		Owner: "none-worker", Authorize: outerAuthorize, ModuleCall: bridge.Call, DiscardCall: bridge.Discard,
		Tools: func(context.Context, domain.Run, domain.Principal, string) ([]provider.Tool, error) {
			return []provider.Tool{
				{
					Type:        "function",
					Name:        "module_call",
					Description: "Book requested slot",
					Parameters:  json.RawMessage(`{"type":"object"}`),
				},
			}, nil
		},
	})
	request := runRequest("", "", "Book Andrey at 15:00")
	request.Interaction.Approvals = true
	run := f.createRun(t, request, "none-revocation")
	require.NoError(t, engine.Execute(t.Context(), run.ID))
	waiting := f.run(t, run.ID)
	require.Equal(t, domain.WaitingApproval, waiting.State)
	require.NotNil(t, waiting.Approval)
	require.Equal(t, "authority_changed", waiting.Approval.FailureCode)
	service.mu.Lock()
	require.Empty(t, service.prepared)
	require.Empty(t, service.executed)
	service.mu.Unlock()
	restored.Store(true)
	code, raw := f.request(
		t,
		"alice",
		"POST",
		"/v1/runs/"+run.ID+"/input",
		"fresh",
		domain.RunInput{Kind: "approval", ApprovalID: waiting.Approval.ID, Decision: "approve"},
	)
	require.Equal(t, 200, code, string(raw))
	require.NoError(t, engine.Execute(t.Context(), run.ID))
	require.Equal(t, domain.Completed, f.run(t, run.ID).State)
	service.mu.Lock()
	require.Empty(t, service.prepared)
	require.Equal(t, []string{waiting.Approval.OperationID}, service.executed)
	service.mu.Unlock()
}

func testRevokedEngine(t *testing.T, replyMode string) {
	t.Helper()
	f := newFixture(t)
	service, registry := newBookingService(t, false)
	model, _ := mutationWorker(t, f, registry)
	p := domain.Principal{
		Issuer:  "https://test.identity",
		Subject: "alice",
		Actor:   "alice",
		OrgID:   "test-org",
		Permissions: map[string]bool{
			"runs.create":            true,
			"chat.read":              true,
			"booking.massage.access": true,
			"booking.massage.book":   true,
		},
	}
	var revoked atomic.Bool
	var outerAuthorize app.RunAuthorizer = func(context.Context, domain.Run) (domain.Principal, string, error) {
		return p, "delegated-user-token", nil
	}
	bridgeAuthorize := func(ctx context.Context, run domain.Run) (domain.Principal, string, error) {
		if revoked.Load() {
			return domain.Principal{}, "", domain.Forbidden()
		}
		return outerAuthorize(ctx, run)
	}
	client, err := provider.NewResponses(model.server.URL, "test-provider-key", model.server.Client())
	require.NoError(t, err)
	newEngine := func() *runs.Engine {
		bridge := app.NewModuleBridge(registry, &operations.Coordinator{Pool: f.pool}, f.repository, bridgeAuthorize)
		f.repository.SetApprovalHandler(bridge.ApproveTx)
		return runs.New(f.repository, client, runs.Options{
			Owner: "revocation-worker", Authorize: outerAuthorize, ModuleCall: bridge.Call, DiscardCall: bridge.Discard,
			Tools: func(context.Context, domain.Run, domain.Principal, string) ([]provider.Tool, error) {
				return []provider.Tool{
					{
						Type:        "function",
						Name:        "module_call",
						Description: "Book requested slot",
						Parameters:  json.RawMessage(`{"type":"object"}`),
					},
				}, nil
			},
		})
	}
	engine := newEngine()
	request := runRequest("", "", "Book Andrey at 15:00")
	request.Interaction.Approvals = true
	request.Interaction.ReplyMode = replyMode
	run := f.createRun(t, request, "engine-revocation")
	require.NoError(t, engine.Execute(t.Context(), run.ID))
	first := f.run(t, run.ID).Approval
	require.NotNil(t, first)
	if replyMode == domain.ReplyModeAgent {
		model.respond = func(index int) any {
			if index <= 3 {
				current := f.run(t, run.ID)
				return conversationCall(
					"interaction_resolve",
					"decision-"+current.ConversationInput.ID,
					domain.InteractionResolution{
						InputID: current.ConversationInput.ID, ApprovalID: current.Approval.ID,
						ArgumentHash: current.Approval.ArgumentHash, Decision: "approve",
					},
				)
			}
			return finalResponse("Booking complete")
		}
	}
	consent := func(key, id string) {
		t.Helper()
		if replyMode == domain.ReplyModeAgent {
			sendConversationText(t, f, run.ID, key, "Yes, book the exact original slot")
			return
		}
		code, raw := f.request(
			t,
			"alice",
			"POST",
			"/v1/runs/"+run.ID+"/input",
			key,
			domain.RunInput{Kind: "approval", ApprovalID: id, Decision: "approve"},
		)
		require.Equal(t, 200, code, string(raw))
	}
	consent("first", first.ID)
	revoked.Store(true)
	require.NoError(t, engine.Execute(t.Context(), run.ID))
	waiting := f.run(t, run.ID)
	require.Equal(t, domain.WaitingApproval, waiting.State)
	require.Equal(t, "authority_changed", waiting.Approval.FailureCode)
	require.NotEqual(t, first.ID, waiting.Approval.ID)
	require.Equal(t, first.OperationID, waiting.Approval.OperationID)
	require.Equal(t, first.Call, waiting.Approval.Call)
	code, raw := f.request(t, "alice", "GET", "/v1/runs/"+run.ID+"/events", "", nil)
	require.Equal(t, 200, code, string(raw))
	require.Contains(t, string(raw), "authority_changed")
	revoked.Store(false)
	engine = newEngine()
	if replyMode == domain.ReplyModeProtocol {
		code, raw = f.request(
			t,
			"alice",
			"POST",
			"/v1/runs/"+run.ID+"/input",
			"old",
			domain.RunInput{Kind: "approval", ApprovalID: first.ID, Decision: "approve"},
		)
		require.Equal(t, 409, code, string(raw))
	}
	service.mu.Lock()
	require.Empty(t, service.executed)
	service.mu.Unlock()
	consent("new", waiting.Approval.ID)
	require.NoError(t, engine.Execute(t.Context(), run.ID))
	require.Equal(t, domain.Completed, f.run(t, run.ID).State)
	service.mu.Lock()
	require.Equal(t, []string{first.OperationID}, service.prepared)
	require.Equal(t, []string{first.OperationID}, service.executed)
	service.mu.Unlock()
}
