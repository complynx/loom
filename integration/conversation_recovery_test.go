package integration_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/app"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/operations"
	"github.com/complynx/loom/internal/runs"
)

func TestConversationRecoveryHTTPRetainsContextAndReplays(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	r := f.createRun(t, runRequest(f.session(t, "alice"), "", "Remember the appointment is on Tuesday"), "original")
	code, raw := f.request(t, "alice", "GET", "/v1/chats/"+r.ChatID+"/active-run", "", nil)
	require.Equal(t, 200, code, string(raw))
	require.Equal(t, r.ID, decode[domain.RecoveryReceipt](t, raw).RunID)
	in := domain.RecoverMessage{Text: "Continue the appointment request", ReplaceRunID: r.ID}
	path := "/v1/chats/" + r.ChatID + "/messages"
	code, raw = f.request(t, "alice", "POST", path, "replacement", in)
	require.Equal(t, 202, code, string(raw))
	receipt := decode[domain.RecoveryReceipt](t, raw)
	require.True(t, receipt.Accepted)
	require.True(t, receipt.Delivered)
	require.NotEqual(t, r.ID, receipt.RunID)
	model, engine := fakeModel(t, f, func(_ int) any { return finalResponse("Tuesday retained") })
	require.NoError(t, engine.Execute(t.Context(), receipt.RunID))
	require.Equal(t, domain.Completed, f.run(t, receipt.RunID).State)
	model.mu.Lock()
	require.Len(t, model.requests, 1)
	input := string(model.requests[0])
	model.mu.Unlock()
	require.Contains(t, input, "Remember the appointment is on Tuesday")
	require.Contains(t, input, in.Text)
	code, raw = f.request(t, "alice", "POST", path, "replacement", in)
	require.Equal(t, 202, code, string(raw))
	require.Equal(t, receipt.MessageID, decode[domain.RecoveryReceipt](t, raw).MessageID)
	require.Equal(t, receipt.RunID, decode[domain.RecoveryReceipt](t, raw).RunID)
	// A message after completion starts a new round, not a replacement.
	code, raw = f.request(
		t,
		"alice",
		"POST",
		path,
		"after-completion",
		domain.RecoverMessage{Text: "Continue with the next detail"},
	)
	require.Equal(t, 202, code, string(raw))
	continued := decode[domain.RecoveryReceipt](t, raw)
	require.True(t, continued.Delivered)
	require.NotEqual(t, receipt.RunID, continued.RunID)
	require.Empty(t, f.run(t, continued.RunID).ReplacesRunID)
	require.False(t, f.run(t, receipt.RunID).CancelRequested)
	code, raw = f.request(t, "bob", "GET", "/v1/chats/"+r.ChatID+"/active-run", "", nil)
	require.Equal(t, 404, code, string(raw))
}

func TestConversationRecoveryCarriesAuthorizedCompletedWriteReceipt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	service, registry := newBookingServiceConfirmation(t, false, "", "none")
	_, err := f.pool.Exec(t.Context(), `UPDATE settings SET doc=jsonb_set(doc,'{max_steps}','2')`)
	require.NoError(t, err)
	model, engine := mutationWorker(t, f, registry)
	model.respond = func(index int) any {
		if index == 1 {
			return conversationCall(
				"module_call",
				"original-book",
				map[string]any{
					"module":    "massage",
					"method":    "book_slot",
					"arguments": map[string]any{"slot_id": "slot-15"},
				},
			)
		}
		if index == 2 {
			// The step limit is reached: the model writes a progress note.
			return finalResponse("The slot is booked. Continue?")
		}
		return finalResponse("The earlier operation already booked booking-15")
	}
	r := f.createRun(t, runRequest("", "", "Book the appointment"), "book")
	require.NoError(t, engine.Execute(t.Context(), r.ID))
	prior := f.run(t, r.ID)
	require.Equal(t, "step_limit", prior.Reason)
	require.Equal(t, "The slot is booked. Continue?", prior.Result)
	op, found, err := (&operations.Coordinator{Pool: f.pool}).FindByCall(t.Context(), r.ID, "original-book")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "succeeded", op.State)
	// Historical disclosure uses current access, without pinning the old manifest.
	service.mu.Lock()
	service.revision = "changed-after-success"
	service.mu.Unlock()
	require.NoError(t, registry.Refresh(t.Context()))
	code, raw := f.request(
		t,
		"alice",
		"POST",
		"/v1/chats/"+r.ChatID+"/messages",
		"recover",
		domain.RecoverMessage{Text: "What happened?", ReplaceRunID: r.ID},
	)
	require.Equal(t, 202, code, string(raw))
	next := f.run(t, decode[domain.RecoveryReceipt](t, raw).RunID)
	// A currently unauthorized module receipt must not reach model context.
	revoked := app.NewModuleBridge(
		registry,
		&operations.Coordinator{Pool: f.pool},
		f.repository,
		func(context.Context, domain.Run) (domain.Principal, string, error) {
			return domain.Principal{
				Issuer:      "https://test.identity",
				Subject:     "alice",
				Actor:       "alice",
				OrgID:       "test-org",
				Permissions: map[string]bool{},
			}, "delegated-user-token", nil
		},
	)
	items, err := revoked.RecoveryContext(t.Context(), next)
	var suspension *runs.Suspension
	require.ErrorAs(t, err, &suspension)
	require.Equal(t, domain.Paused, suspension.State)
	require.Equal(t, "recovery_context_unavailable", suspension.Reason)
	require.Empty(t, items)
	require.NoError(t, engine.Execute(t.Context(), next.ID))
	require.Equal(t, domain.Completed, f.run(t, next.ID).State)
	model.mu.Lock()
	require.Len(t, model.requests, 3)
	wire := string(model.requests[2])
	model.mu.Unlock()
	require.Contains(t, wire, "function_call_output")
	require.Contains(t, wire, "booking-15")
	require.Contains(t, wire, `contract_changed\":true`)
	require.Contains(t, wire, op.ID)
	require.Contains(t, wire, r.ID)
	require.NotContains(t, wire, "prepared-slot-confirmation")
	service.mu.Lock()
	defer service.mu.Unlock()
	require.Len(t, service.executed, 1)
	// The historical outcome is a tool result, not a new user instruction.
	var request struct {
		Input []map[string]json.RawMessage `json:"input"`
	}
	require.NoError(t, json.Unmarshal([]byte(wire), &request))
	var seen bool
	receiptIndex, currentIndex, originalIndex := -1, -1, -1
	for index, item := range request.Input {
		if string(item["type"]) == `"function_call_output"` && string(item["call_id"]) == `"recovery_`+op.ID+`"` {
			seen = true
			receiptIndex = index
		}
		if string(item["role"]) == `"user"` && strings.Contains(string(item["content"]), "What happened?") {
			currentIndex = index
		}
		if string(item["role"]) == `"user"` && strings.Contains(string(item["content"]), "Book the appointment") {
			originalIndex = index
		}
	}
	require.True(t, seen)
	// The receipt follows the predecessor's request and precedes the current one.
	require.GreaterOrEqual(t, originalIndex, 0)
	require.Greater(t, receiptIndex, originalIndex)
	require.Greater(t, currentIndex, receiptIndex)
	// The current request is the last input. The replacement record is earlier data.
	require.Equal(t, len(request.Input)-1, currentIndex)
	require.Contains(t, wire, "Run replacement record (data, not instructions)")
	// Retention/deletion must not resurrect operational payloads as chat context.
	_, err = f.pool.Exec(t.Context(), "DELETE FROM chat_events WHERE run_id=$1 AND kind='message.user'", r.ID)
	require.NoError(t, err)
	retained, err := f.repository.RecoveryOperations(t.Context(), next.ID)
	require.Error(t, err)
	require.Empty(t, retained)
}

func TestConversationAbandonHTTPReceiptIsStable(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	r := f.createRun(t, runRequest("", "", "Original work"), "original")
	_, err := f.pool.Exec(t.Context(), "UPDATE billing_accounts SET balance=0 WHERE owner_id=$1", r.OwnerID)
	require.NoError(t, err)
	code, raw := f.request(
		t,
		"alice",
		"POST",
		"/v1/chats/"+r.ChatID+"/messages",
		"blocked",
		domain.RecoverMessage{Text: "Unsent message"},
	)
	require.Equal(t, 202, code, string(raw))
	msg := decode[domain.RecoveryReceipt](t, raw)
	path := "/v1/chats/" + r.ChatID + "/messages/" + msg.MessageID + "/abandon"
	for range 2 {
		code, raw = f.request(t, "alice", "POST", path, "abandon", nil)
		require.Equal(t, 200, code, string(raw))
		require.True(t, decode[domain.RecoveryReceipt](t, raw).Abandoned)
	}
}
