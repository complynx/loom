package integration_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
)

func conversationFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	_, err := f.pool.Exec(t.Context(), `UPDATE settings SET doc=jsonb_set(doc,'{max_steps}','40')`)
	require.NoError(t, err)
	return f
}

func conversationCall(name, id string, args any) any {
	raw, _ := json.Marshal(args)
	return map[string]any{
		"id":     id,
		"status": "completed",
		"usage":  map[string]int{"input_tokens": 10, "output_tokens": 5},
		"output": []any{map[string]any{"type": "function_call", "name": name, "call_id": id, "arguments": string(raw)}},
	}
}

func sendConversationText(t *testing.T, f *fixture, runID, key, text string) {
	t.Helper()
	code, raw := f.request(
		t,
		"alice",
		"POST",
		"/v1/runs/"+runID+"/input",
		key,
		map[string]string{"kind": "steering", "text": text},
	)
	require.Equal(t, 200, code, string(raw))
	require.Contains(t, string(raw), "input_id")
}

func TestConversationClarifiesAcrossMessagesThenResolvesExactAction(t *testing.T) {
	t.Parallel()
	for _, decision := range []string{"approve", "reject"} {
		t.Run(decision, func(t *testing.T) {
			t.Parallel()
			f := conversationFixture(t)
			service, registry := newBookingService(t, false)
			model, engine := mutationWorker(t, f, registry)
			request := runRequest("", "", "Book Andrey at 15:00")
			request.Interaction.ReplyMode = "agent"
			request.Interaction.Approvals = true
			run := f.createRun(t, request, "conversation")
			model.respond = func(index int) any {
				switch index {
				case 1:
					return conversationCall(
						"module_call",
						"book",
						map[string]any{
							"module":    "massage",
							"method":    "book_slot",
							"arguments": map[string]string{"slot_id": "slot-15"},
						},
					)
				case 2:
					return conversationCall(
						"question",
						"follow-up",
						map[string]any{
							"text":       "Стоимость 10 евро. Подтвердить?",
							"choices":    []any{},
							"allow_text": false,
						},
					)
				case 3:
					return finalResponse("Андрей, 15:00, 10 евро. Подтвердить бронирование?")
				case 4:
					current := f.run(t, run.ID)
					return conversationCall("interaction_resolve", "resolve", domain.InteractionResolution{
						InputID:      current.ConversationInput.ID,
						ApprovalID:   current.Approval.ID,
						ArgumentHash: current.Approval.ArgumentHash,
						Decision:     decision,
					})
				default:
					return finalResponse("Результат: " + decision)
				}
			}
			require.NoError(t, engine.Execute(t.Context(), run.ID))
			original := f.run(t, run.ID).Approval
			require.NotNil(t, original)
			for i, text := range []string{"Сколько это стоит?", "Напомни, у кого запись?"} {
				sendConversationText(t, f, run.ID, text, text)
				require.NoError(t, engine.Execute(t.Context(), run.ID))
				waiting := f.run(t, run.ID)
				require.Equal(t, domain.WaitingInput, waiting.State)
				require.Equal(t, original.ID, waiting.Approval.ID)
				require.Empty(t, waiting.Approval.Decision)
				service.mu.Lock()
				require.Empty(t, service.executed, "clarification %d must not dispatch", i)
				service.mu.Unlock()
			}
			text := "Да, запиши меня к Андрею на 15:00"
			if decision == "reject" {
				text = "Нет, не записывай"
			}
			sendConversationText(t, f, run.ID, "decision", text)
			engine = mutationEngine(t, f, registry, model)
			require.NoError(t, engine.Execute(t.Context(), run.ID))
			final := f.run(t, run.ID)
			require.Equal(t, domain.Completed, final.State)
			require.Equal(t, original.ID, final.Approval.ID)
			service.mu.Lock()
			if decision == "approve" {
				require.Equal(t, []string{original.ID}, service.executed)
			} else {
				require.Empty(t, service.executed)
			}
			service.mu.Unlock()
			model.mu.Lock()
			require.Contains(t, string(model.requests[3]), text)
			require.Contains(t, string(model.requests[3]), original.ArgumentHash)
			require.Contains(t, string(model.requests[3]), "latest_input")
			model.mu.Unlock()
		})
	}
}

func TestCriticalActionUsesBinaryOrExactLocalizedText(t *testing.T) {
	t.Parallel()
	for _, variant := range []string{"native", "exact", "other message"} {
		t.Run(variant, func(t *testing.T) {
			t.Parallel()
			f := conversationFixture(t)
			service, registry := newBookingServiceMode(t, false, "protocol")
			model, engine := mutationWorker(t, f, registry)
			phrase := "Подтверждаю запись к Андрею на 15:00"
			model.respond = func(index int) any {
				if index == 1 {
					return conversationCall(
						"module_call",
						"book",
						map[string]any{
							"module":            "massage",
							"method":            "book_slot",
							"arguments":         map[string]string{"slot_id": "slot-15"},
							"confirmation_text": phrase,
						},
					)
				}
				return finalResponse("Actual outcome received")
			}
			request := runRequest("", "", "Book Andrey")
			request.Interaction.ReplyMode = "agent"
			request.Interaction.Approvals = variant == "native"
			run := f.createRun(t, request, "critical")
			require.NoError(t, engine.Execute(t.Context(), run.ID))
			waiting := f.run(t, run.ID)
			if variant == "native" {
				require.Equal(t, "protocol", waiting.Approval.ReplyMode)
				// Ordinary chat can clarify while the native binary form remains pending.
				sendConversationText(t, f, run.ID, "clarify", "Сколько стоит?")
				require.NoError(t, engine.Execute(t.Context(), run.ID))
				code, raw := f.request(
					t,
					"alice",
					"POST",
					"/v1/runs/"+run.ID+"/input",
					"binary",
					domain.RunInput{Kind: "approval", ApprovalID: waiting.Approval.ID, Decision: "approve"},
				)
				require.Equal(t, 200, code, string(raw))
			} else {
				require.Equal(t, "exact_text", waiting.Approval.ReplyMode)
				require.Equal(t, phrase, waiting.Approval.ConfirmationText)
				text := phrase
				if variant == "other message" {
					text = "Да, но какая цена?"
				}
				sendConversationText(t, f, run.ID, "reply", text)
			}
			require.NoError(t, engine.Execute(t.Context(), run.ID))
			require.Equal(t, domain.Completed, f.run(t, run.ID).State)
			service.mu.Lock()
			if variant == "other message" {
				require.Empty(t, service.executed)
			} else {
				require.Equal(t, []string{waiting.Approval.ID}, service.executed)
			}
			service.mu.Unlock()
		})
	}
}
