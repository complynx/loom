package integration_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
)

func TestConversationQualifiedConsentPreparesNewProposal(t *testing.T) {
	t.Parallel()
	f := conversationFixture(t)
	service, registry := newBookingService(t, false)
	model, engine := mutationWorker(t, f, registry)
	request := runRequest("", "", "Book Andrey at 15:00")
	request.Interaction.ReplyMode = "agent"
	run := f.createRun(t, request, "amend")
	model.respond = func(index int) any {
		switch index {
		case 1, 3:
			slot, callID := "slot-15", "original"
			if index == 3 {
				slot, callID = "slot-16", "replacement"
			}
			return conversationCall(
				"module_call",
				callID,
				map[string]any{
					"module":    "massage",
					"method":    "book_slot",
					"arguments": map[string]string{"slot_id": slot},
				},
			)
		case 2, 4:
			current := f.run(t, run.ID)
			decision := "reject"
			if index == 4 {
				decision = "approve"
			}
			return conversationCall("interaction_resolve", decision, domain.InteractionResolution{
				InputID:      current.ConversationInput.ID,
				ApprovalID:   current.Approval.ID,
				ArgumentHash: current.Approval.ArgumentHash,
				Decision:     decision,
			})
		default:
			return finalResponse("Confirmed replacement")
		}
	}
	require.NoError(t, engine.Execute(t.Context(), run.ID))
	original := f.run(t, run.ID).Approval
	sendConversationText(t, f, run.ID, "qualified", "Да, но перенеси на 16:00")
	require.NoError(t, engine.Execute(t.Context(), run.ID))
	replacement := f.run(t, run.ID)
	require.Equal(t, domain.WaitingApproval, replacement.State)
	require.NotEqual(t, original.ID, replacement.Approval.ID)
	require.JSONEq(t, `{"slot_id":"slot-16"}`, string(replacement.Approval.Arguments))
	require.Empty(t, replacement.Approval.Decision)
	service.mu.Lock()
	require.Empty(t, service.executed)
	service.mu.Unlock()
	sendConversationText(t, f, run.ID, "consent", "Подтверждаю запись на 16:00")
	require.NoError(t, engine.Execute(t.Context(), run.ID))
	require.Equal(t, domain.Completed, f.run(t, run.ID).State)
	service.mu.Lock()
	require.Equal(t, []string{replacement.Approval.ID}, service.executed)
	service.mu.Unlock()
}
