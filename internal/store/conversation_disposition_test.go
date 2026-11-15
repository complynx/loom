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

// Each user message is a new interaction. After a terminal run it starts a new
// round with the current run cap; the wallet is the shared limit of the owner.
func TestRecoveryMessageAfterTerminalRunStartsNewRound(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	p := principal("new-round")
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
	// An empty wallet blocks the new round. The locator reports the same blocker.
	_, err = s.Pool().Exec(t.Context(), "UPDATE billing_accounts SET balance=0 WHERE owner_id=$1", r.OwnerID)
	require.NoError(t, err)
	message := domain.RecoverMessage{Text: "next question"}
	blocked, err := s.RecoverChatMessage(t.Context(), p, r.ChatID, "retained", message)
	require.NoError(t, err)
	require.Equal(t, "credits_exhausted", blocked.Reason)
	locator, err := s.ActiveChatRun(t.Context(), p, r.ChatID)
	require.NoError(t, err)
	require.Equal(t, blocked.Reason, locator.Reason)
	require.Equal(t, blocked.NextAction, locator.NextAction)
	// Ordinary creation cannot pass a retained message.
	fresh := request()
	fresh.ChatID = r.ChatID
	fresh.Input.Text = "Later request"
	_, err = s.CreateRun(t.Context(), p, "distinct", fresh)
	code(t, err, "pending_conversation_messages")
	replay, err := s.CreateRun(t.Context(), p, "original", original)
	require.NoError(t, err)
	require.Equal(t, r.ID, replay.ID)
	// The spent allowance of the completed run does not limit the new round.
	_, err = s.Pool().Exec(t.Context(), "UPDATE billing_accounts SET balance=10000 WHERE owner_id=$1", r.OwnerID)
	require.NoError(t, err)
	delivered, err := s.RecoverChatMessage(t.Context(), p, r.ChatID, "retained", message)
	require.NoError(t, err)
	require.True(t, delivered.Delivered)
	next, err := s.GetRun(t.Context(), p, delivered.RunID)
	require.NoError(t, err)
	require.NotEqual(t, r.ID, next.ID)
	require.Empty(t, next.ReplacesRunID)
	require.Greater(t, next.MaxMicrocredits, r.MaxMicrocredits)
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
	// A message without a target after a terminal run starts an unlinked new round.
	require.Empty(t, successor.ReplacesRunID)
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
func TestRecoveryDeliveryIgnoresCallerIdempotencyKeys(t *testing.T) {
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
	var text string
	require.NoError(
		t,
		s.Pool().
			QueryRow(t.Context(), "SELECT doc->>'text' FROM run_inputs WHERE id=$1", delivered.InputID).
			Scan(&text),
	)
	require.Equal(t, in.Text, text)
}

// A run allowance bounds one action. When a run stops because its own allowance
// or the wallet cannot pay the next step, the next message continues the chat in
// a new run.
func TestRecoveryMessageReplacesRunPausedForMoney(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	for _, reason := range []string{"budget_exhausted", "credits_exhausted"} {
		p := principal("paused-" + reason)
		r, err := s.CreateRun(t.Context(), p, "create", request())
		require.NoError(t, err)
		r, err = s.ClaimRun(t.Context(), r.ID, "worker", time.Minute)
		require.NoError(t, err)
		require.NoError(t, s.Transition(t.Context(), r.ID, "worker", r.Fence, domain.Paused, reason, ""))
		located, err := s.ActiveChatRun(t.Context(), p, r.ChatID)
		require.NoError(t, err)
		require.Equal(t, r.ID, located.RunID)
		require.Equal(t, "accepted", located.Status)
		// A spent run allowance means work without a result: the user decides.
		action := "send_message"
		if reason == "budget_exhausted" {
			action = "ask_user_to_continue"
		}
		require.Equal(t, action, located.NextAction)
		receipt, err := s.RecoverChatMessage(t.Context(), p, r.ChatID, "next", domain.RecoverMessage{Text: "next"})
		require.NoError(t, err)
		require.True(t, receipt.Delivered)
		require.Equal(t, "queued", receipt.Status)
		next, err := s.GetRun(t.Context(), p, receipt.RunID)
		require.NoError(t, err)
		// The unfinished run is superseded, and its successor gets its receipts.
		require.Equal(t, r.ID, next.ReplacesRunID)
		require.GreaterOrEqual(t, next.MaxMicrocredits, r.MaxMicrocredits)
		previous, err := s.GetRun(t.Context(), p, r.ID)
		require.NoError(t, err)
		require.Equal(t, domain.Cancelled, previous.State)
	}
}
