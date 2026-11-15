package store_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
)

func TestRecoveryAbandonUnblocksWithoutDeliveringWithdrawnText(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	p := principal("disposition")
	r, err := s.CreateRun(t.Context(), p, "create", request())
	require.NoError(t, err)
	_, err = s.Pool().Exec(t.Context(), "UPDATE billing_accounts SET balance=0 WHERE owner_id=$1", r.OwnerID)
	require.NoError(t, err)
	first, err := s.RecoverChatMessage(
		t.Context(),
		p,
		r.ChatID,
		"withdraw",
		domain.RecoverMessage{Text: "withdrawn text"},
	)
	require.NoError(t, err)
	require.Equal(t, "credits_exhausted", first.Reason)
	_, err = s.RecoverChatMessage(t.Context(), p, r.ChatID, "later", domain.RecoverMessage{Text: "later text"})
	require.NoError(t, err)
	foreign := principal(p.Subject)
	foreign.Actor = "another-actor"
	_, err = s.AbandonChatMessage(t.Context(), foreign, r.ChatID, first.MessageID, "deny-abandon")
	code(t, err, "not_found")
	for range 2 {
		out, e := s.AbandonChatMessage(t.Context(), p, r.ChatID, first.MessageID, "abandon")
		require.NoError(t, e)
		require.True(t, out.Abandoned)
		require.False(t, out.Delivered)
	}
	_, err = s.Pool().Exec(t.Context(), "UPDATE billing_accounts SET balance=10000 WHERE owner_id=$1", r.OwnerID)
	require.NoError(t, err)
	firstRetry, err := s.RecoverChatMessage(
		t.Context(),
		p,
		r.ChatID,
		"withdraw",
		domain.RecoverMessage{Text: "withdrawn text"},
	)
	require.NoError(t, err)
	require.Equal(t, "abandoned", firstRetry.Status)
	later, err := s.RecoverChatMessage(t.Context(), p, r.ChatID, "later", domain.RecoverMessage{Text: "later text"})
	require.NoError(t, err)
	require.True(t, later.Delivered)
	_, err = s.AbandonChatMessage(t.Context(), p, r.ChatID, later.MessageID, "too-late")
	code(t, err, "message_already_delivered")
	var text string
	require.NoError(
		t,
		s.Pool().
			QueryRow(t.Context(), "SELECT request->>'text' FROM conversation_messages WHERE id=$1", first.MessageID).
			Scan(&text),
	)
	require.Equal(t, "withdrawn text", text)
	inputs, err := s.History(t.Context(), p, r.ChatID, 0, 100)
	require.NoError(t, err)
	for _, event := range inputs {
		require.NotEqual(t, "withdrawn text", event.Text)
	}
}

func TestRecoveryPendingMessagesPreventNewAllowanceAdmission(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	p := principal("allowance-boundary")
	original := request()
	original.Budget.MaxMicrocredits = 5
	r, err := s.CreateRun(t.Context(), p, "original", original)
	require.NoError(t, err)
	r, err = s.ClaimRun(t.Context(), r.ID, "worker", time.Minute)
	require.NoError(t, err)
	step, err := s.BeginStep(t.Context(), r.ID, "worker", r.Fence, "model", json.RawMessage(`{}`), 5)
	require.NoError(t, err)
	require.NoError(
		t,
		s.CompleteStep(
			t.Context(),
			r.ID,
			"worker",
			r.Fence,
			step.ID,
			json.RawMessage(`{}`),
			domain.Usage{Microcredits: 5},
		),
	)
	require.NoError(t, s.Transition(t.Context(), r.ID, "worker", r.Fence, domain.Completed, "", "done"))
	message := domain.RecoverMessage{Text: "continue original work"}
	blocked, err := s.RecoverChatMessage(t.Context(), p, r.ChatID, "retained", message)
	require.NoError(t, err)
	require.Equal(t, "budget_exhausted", blocked.Reason)
	locator, err := s.ActiveChatRun(t.Context(), p, r.ChatID)
	require.NoError(t, err)
	require.Equal(t, "budget_exhausted", locator.Reason)
	fresh := request()
	fresh.ChatID = r.ChatID
	fresh.Input.Text = "Explicitly authorized distinct request"
	_, err = s.CreateRun(t.Context(), p, "distinct", fresh)
	code(t, err, "pending_conversation_messages")
	replay, err := s.CreateRun(t.Context(), p, "original", original)
	require.NoError(t, err)
	require.Equal(t, r.ID, replay.ID)
	_, err = s.AbandonChatMessage(t.Context(), p, r.ChatID, blocked.MessageID, "withdraw")
	require.NoError(t, err)
	next, err := s.CreateRun(t.Context(), p, "distinct", fresh)
	require.NoError(t, err)
	require.NotEqual(t, r.ID, next.ID)
	retained, err := s.RecoverChatMessage(t.Context(), p, r.ChatID, "retained", message)
	require.NoError(t, err)
	require.True(t, retained.Abandoned)
	var inputs int
	require.NoError(
		t,
		s.Pool().QueryRow(t.Context(), "SELECT count(*) FROM run_inputs WHERE run_id=$1", next.ID).Scan(&inputs),
	)
	require.Zero(t, inputs)
}

func TestRecoveryUsesAdmissionOrderAndRejectsStaleTargetBeforeAcceptance(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	p := principal("admission-order")
	r, err := s.CreateRun(t.Context(), p, "create", request())
	require.NoError(t, err)
	root := r
	for _, key := range []string{"first", "second"} {
		next, e := s.RecoverChatMessage(
			t.Context(),
			p,
			r.ChatID,
			key,
			domain.RecoverMessage{Text: key, ReplaceRunID: r.ID},
		)
		require.NoError(t, e)
		r, err = s.GetRun(t.Context(), p, next.RunID)
		require.NoError(t, err)
	}
	_, err = s.Pool().Exec(t.Context(), "UPDATE runs SET created_at=now()-interval '1 day' WHERE id=$1", r.ID)
	require.NoError(t, err)
	located, err := s.ActiveChatRun(t.Context(), p, r.ChatID)
	require.NoError(t, err)
	require.Equal(t, r.ID, located.RunID)
	_, err = s.RecoverChatMessage(
		t.Context(),
		p,
		r.ChatID,
		"stale",
		domain.RecoverMessage{Text: "stale", ReplaceRunID: root.ID},
	)
	code(t, err, "replacement_target_changed")
	_, err = s.Control(t.Context(), p, r.ID, "cancel-current", "cancel")
	require.NoError(t, err)
	located, err = s.ActiveChatRun(t.Context(), p, r.ChatID)
	require.NoError(t, err)
	require.Equal(t, r.ID, located.RunID)
	require.Equal(t, "terminal", located.Status)
	require.Equal(t, "send_message", located.NextAction)
	next, err := s.RecoverChatMessage(t.Context(), p, r.ChatID, "continue", domain.RecoverMessage{Text: "continue"})
	require.NoError(t, err)
	require.True(t, next.Delivered)
	successor, err := s.GetRun(t.Context(), p, next.RunID)
	require.NoError(t, err)
	require.Equal(t, r.ID, successor.ReplacesRunID)
	var rejected int
	require.NoError(
		t,
		s.Pool().
			QueryRow(t.Context(), "SELECT count(*) FROM conversation_messages WHERE chat_id=$1 AND key='stale'", r.ChatID).
			Scan(&rejected),
	)
	require.Zero(t, rejected)
}

func TestRecoveryEmptyChatRequiresFirstRunWithoutAcceptingText(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	p := principal("empty-recovery")
	chat, err := s.CreateChat(t.Context(), p)
	require.NoError(t, err)
	_, err = s.RecoverChatMessage(t.Context(), p, chat.ID, "first", domain.RecoverMessage{Text: "first message"})
	code(t, err, "no_run")
	locator, err := s.ActiveChatRun(t.Context(), p, chat.ID)
	require.NoError(t, err)
	require.Equal(t, "idle", locator.Status)
	require.Equal(t, "create_run", locator.NextAction)
}

// A caller can learn a message ID and reuse it as an ordinary idempotency key.
// That key must neither block nor satisfy the delivery of the inbox message.
func TestRecoveryDeliveryIgnoresCallerKeysAndLocatorMatchesDelivery(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	p := principal("key-spaces")
	r, err := s.CreateRun(t.Context(), p, "create", request())
	require.NoError(t, err)
	_, err = s.Pool().Exec(t.Context(), "UPDATE billing_accounts SET balance=0 WHERE owner_id=$1", r.OwnerID)
	require.NoError(t, err)
	in := domain.RecoverMessage{Text: "retained"}
	blocked, err := s.RecoverChatMessage(t.Context(), p, r.ChatID, "message", in)
	require.NoError(t, err)
	require.False(t, blocked.Delivered)
	other, err := s.AddInput(
		t.Context(),
		p,
		r.ID,
		blocked.MessageID,
		domain.RunInput{Kind: domain.InputSteering, Text: "other"},
	)
	require.NoError(t, err)
	_, err = s.Pool().Exec(t.Context(), "UPDATE billing_accounts SET balance=10000 WHERE owner_id=$1", r.OwnerID)
	require.NoError(t, err)
	delivered, err := s.RecoverChatMessage(t.Context(), p, r.ChatID, "message", in)
	require.NoError(t, err)
	require.True(t, delivered.Delivered)
	require.NotEqual(t, other.ConversationInput.ID, delivered.InputID)
	// The locator reports the blocker that the next message would meet. A resume
	// cannot help a run whose allowance is spent.
	_, err = s.Pool().Exec(
		t.Context(),
		"UPDATE runs SET state='paused',reason='budget_exhausted',spent_microcredits=max_microcredits WHERE id=$1",
		r.ID,
	)
	require.NoError(t, err)
	located, err := s.ActiveChatRun(t.Context(), p, r.ChatID)
	require.NoError(t, err)
	next, err := s.RecoverChatMessage(t.Context(), p, r.ChatID, "next", domain.RecoverMessage{Text: "next"})
	require.NoError(t, err)
	require.Equal(t, "budget_exhausted", located.Reason)
	require.Equal(t, "request_new_work_allowance", located.NextAction)
	require.Equal(t, located.Reason, next.Reason)
	require.Equal(t, located.NextAction, next.NextAction)
	var text string
	require.NoError(
		t,
		s.Pool().
			QueryRow(t.Context(), "SELECT doc->>'text' FROM run_inputs WHERE id=$1", delivered.InputID).
			Scan(&text),
	)
	require.Equal(t, in.Text, text)
}
