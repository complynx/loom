package store_test

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/store"
)

func TestRecoveryLocatesAndWakesLostStepLimitRun(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	p := principal("lost-pointer")
	r, err := s.CreateRun(t.Context(), p, "create", request())
	require.NoError(t, err)
	r, err = s.ClaimRun(t.Context(), r.ID, "worker", time.Minute)
	require.NoError(t, err)
	_, err = s.Pool().
		Exec(t.Context(), "UPDATE runs SET state='paused',reason='step_limit',lease_owner='',lease_until=NULL WHERE id=$1", r.ID)
	require.NoError(t, err)
	located, err := s.ActiveChatRun(t.Context(), p, r.ChatID)
	require.NoError(t, err)
	require.Equal(t, r.ID, located.RunID)
	require.Equal(t, "accepted", located.Status)
	require.Equal(t, "step_limit", located.Reason)
	require.Equal(t, "ask_user_to_continue", located.NextAction)
	in := domain.RecoverMessage{Text: "Please continue"}
	receipt, err := s.RecoverChatMessage(t.Context(), p, r.ChatID, "msg-1", in)
	require.NoError(t, err)
	require.Equal(t, "queued", receipt.Status)
	require.Equal(t, r.ID, receipt.RunID)
	require.NotEmpty(t, receipt.InputID)
	// A fresh Store simulates a process losing all local recovery state.
	s = store.New(s.Pool(), nil)
	retry, err := s.RecoverChatMessage(t.Context(), p, r.ChatID, "msg-1", in)
	require.NoError(t, err)
	require.Equal(t, receipt.MessageID, retry.MessageID)
	require.Equal(t, receipt.InputID, retry.InputID)
	_, err = s.RecoverChatMessage(t.Context(), p, r.ChatID, "msg-1", domain.RecoverMessage{Text: "different"})
	code(t, err, "idempotency_conflict")
	var count int
	require.NoError(
		t,
		s.Pool().QueryRow(t.Context(), "SELECT count(*) FROM run_inputs WHERE run_id=$1", r.ID).Scan(&count),
	)
	require.Equal(t, 1, count)
}

func TestRecoverySuccessorKeepsContextAndFence(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	p := principal("replacement")
	r := settledRunForDeletion(t.Context(), t, s, p)
	require.NoError(t, s.Transition(t.Context(), r.ID, "worker", r.Fence, domain.Paused, "unusable", ""))
	in := domain.RecoverMessage{Text: "Continue with the same task", ReplaceRunID: r.ID}
	receipt, err := s.RecoverChatMessage(t.Context(), p, r.ChatID, "replace", in)
	require.NoError(t, err)
	require.Equal(t, "queued", receipt.Status)
	require.NotEqual(t, r.ID, receipt.RunID)
	next, err := s.GetRun(t.Context(), p, receipt.RunID)
	require.NoError(t, err)
	require.Equal(t, r.ChatID, next.ChatID)
	require.Equal(t, r.OwnerID, next.OwnerID)
	require.Equal(t, r.ID, next.ReplacesRunID)
	// The successor gets the current run cap; the wallet is the shared limit.
	require.GreaterOrEqual(t, next.MaxMicrocredits, r.MaxMicrocredits)
	previous, err := s.GetRun(t.Context(), p, r.ID)
	require.NoError(t, err)
	require.Equal(t, domain.Cancelled, previous.State)
	_, err = s.BeginStep(t.Context(), r.ID, "worker", r.Fence, "tool", json.RawMessage(`{}`), 0)
	code(t, err, "lease_lost")
	history, err := s.ExecutionHistory(t.Context(), next.ID, 200)
	require.NoError(t, err)
	var original, successor bool
	for _, event := range history {
		original = original || event.RunID == r.ID && event.Type == "message.user" && event.Text == "hello"
		successor = successor || event.RunID == next.ID && event.Type == "message.user" && event.Text == in.Text
	}
	require.True(t, original)
	require.True(t, successor)
	retry, err := s.RecoverChatMessage(t.Context(), p, r.ChatID, "replace", in)
	require.NoError(t, err)
	require.Equal(t, next.ID, retry.RunID)
}

func TestRecoveryRetainsOrderedMessagesWithoutFunds(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	p := principal("no-funds")
	r, err := s.CreateRun(t.Context(), p, "create", request())
	require.NoError(t, err)
	_, err = s.Pool().Exec(t.Context(), "UPDATE billing_accounts SET balance=0 WHERE owner_id=$1", r.OwnerID)
	require.NoError(t, err)
	first, err := s.RecoverChatMessage(t.Context(), p, r.ChatID, "first", domain.RecoverMessage{Text: "first"})
	require.NoError(t, err)
	require.Equal(t, "credits_exhausted", first.Reason)
	second, err := s.RecoverChatMessage(t.Context(), p, r.ChatID, "second", domain.RecoverMessage{Text: "second"})
	require.NoError(t, err)
	require.Equal(t, "earlier_message_pending", second.Reason)
	_, err = s.Pool().Exec(t.Context(), "UPDATE billing_accounts SET balance=10000 WHERE owner_id=$1", r.OwnerID)
	require.NoError(t, err)
	for _, key := range []string{"first", "second"} {
		receipt, e := s.RecoverChatMessage(t.Context(), p, r.ChatID, key, domain.RecoverMessage{Text: key})
		require.NoError(t, e)
		require.Equal(t, "queued", receipt.Status)
	}
	history, err := s.ExecutionHistory(t.Context(), r.ID, 200)
	require.NoError(t, err)
	var texts []string
	for _, event := range history {
		if event.Type == "input.steering" {
			texts = append(texts, event.Text)
		}
	}
	require.Equal(t, []string{"first", "second"}, texts)
	// A transaction can start earlier but acquire the chat lock later. Input
	// application must use accepted history order rather than transaction time.
	_, err = s.Pool().
		Exec(t.Context(), "UPDATE run_inputs SET created_at=CASE doc->>'text' WHEN 'first' THEN now() ELSE now()-interval '1 hour' END WHERE run_id=$1", r.ID)
	require.NoError(t, err)
	r, err = s.ClaimRun(t.Context(), r.ID, "worker", time.Minute)
	require.NoError(t, err)
	inputs, err := s.PendingInputs(t.Context(), r.ID, "worker", r.Fence)
	require.NoError(t, err)
	require.Len(t, inputs, 2)
	require.Equal(t, "first", inputs[0].Text)
	require.Equal(t, "second", inputs[1].Text)
}

func TestRecoveryCannotReplaceUncertainPaidWork(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	p := principal("unknown")
	r, err := s.CreateRun(t.Context(), p, "create", request())
	require.NoError(t, err)
	r, err = s.ClaimRun(t.Context(), r.ID, "worker", time.Minute)
	require.NoError(t, err)
	step, err := s.BeginStep(t.Context(), r.ID, "worker", r.Fence, "model", json.RawMessage(`{}`), 20)
	require.NoError(t, err)
	require.NoError(t, s.MarkStepUnknown(t.Context(), r.ID, "worker", r.Fence, step.ID))
	in := domain.RecoverMessage{Text: "continue", ReplaceRunID: r.ID}
	for range 2 {
		receipt, e := s.RecoverChatMessage(t.Context(), p, r.ChatID, "unknown", in)
		require.NoError(t, e)
		require.Equal(t, "inference_or_effect_uncertain", receipt.Reason)
	}
	balance, err := s.Balance(t.Context(), p)
	require.NoError(t, err)
	require.EqualValues(t, 20, balance.ReservedMicrocredits)
	var count int
	require.NoError(
		t,
		s.Pool().QueryRow(t.Context(), "SELECT count(*) FROM runs WHERE chat_id=$1", r.ChatID).Scan(&count),
	)
	require.Equal(t, 1, count)
}

func TestRacingRecoveryCreatesOneSuccessor(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	p := principal("racing-replace")
	r, err := s.CreateRun(t.Context(), p, "create", request())
	require.NoError(t, err)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, text := range []string{"first", "second"} {
		wg.Go(func() {
			_, e := s.RecoverChatMessage(
				t.Context(),
				p,
				r.ChatID,
				text,
				domain.RecoverMessage{Text: text, ReplaceRunID: r.ID},
			)
			errs <- e
		})
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	var count int
	require.NoError(
		t,
		s.Pool().QueryRow(t.Context(), "SELECT count(*) FROM runs WHERE chat_id=$1", r.ChatID).Scan(&count),
	)
	require.Equal(t, 2, count)
	var delivered int
	require.NoError(
		t,
		s.Pool().
			QueryRow(t.Context(), "SELECT count(*) FROM conversation_messages WHERE chat_id=$1 AND delivered", r.ChatID).
			Scan(&delivered),
	)
	require.Equal(t, 2, delivered)
}

func TestRecoveryCannotReplaceUncertainBusinessWrite(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	p := principal("unknown-business")
	r, err := s.CreateRun(t.Context(), p, "create", request())
	require.NoError(t, err)
	_, err = s.Pool().
		Exec(t.Context(), `INSERT INTO write_operations(id,run_id,call_id,state,doc) VALUES(gen_random_uuid(),$1,'original-write','unknown','{"receipt":"unresolved original"}')`, r.ID)
	require.NoError(t, err)
	_, err = s.Control(t.Context(), p, r.ID, "cancel", "cancel")
	require.NoError(t, err)
	receipt, err := s.RecoverChatMessage(t.Context(), p, r.ChatID, "recover", domain.RecoverMessage{Text: "continue"})
	require.NoError(t, err)
	require.Equal(t, "inference_or_effect_uncertain", receipt.Reason)
	require.False(t, receipt.Delivered)
	var state, original string
	require.NoError(
		t,
		s.Pool().
			QueryRow(t.Context(), "SELECT state,doc->>'receipt' FROM write_operations WHERE run_id=$1", r.ID).
			Scan(&state, &original),
	)
	require.Equal(t, "unknown", state)
	require.Equal(t, "unresolved original", original)
	var runs int
	require.NoError(
		t,
		s.Pool().QueryRow(t.Context(), "SELECT count(*) FROM runs WHERE chat_id=$1", r.ChatID).Scan(&runs),
	)
	require.Equal(t, 1, runs)
}

func TestRecoveryAuthorizationAndDeletion(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	p := principal("authorized-recovery")
	r, err := s.CreateRun(t.Context(), p, "create", request())
	require.NoError(t, err)
	for _, kind := range []string{"owner", "org", "actor", "permission"} {
		foreign := principal(p.Subject)
		switch kind {
		case "owner":
			foreign.Subject = "foreign"
		case "org":
			foreign.OrgID = "foreign"
		case "actor":
			foreign.Actor = "foreign"
		case "permission":
			delete(foreign.Permissions, "runs.control")
		}
		_, e := s.RecoverChatMessage(t.Context(), foreign, r.ChatID, kind, domain.RecoverMessage{Text: "private"})
		expected := "not_found"
		if kind == "permission" {
			expected = "forbidden"
		}
		code(t, e, expected)
	}
	var rejectedRows int
	require.NoError(
		t,
		s.Pool().
			QueryRow(t.Context(), "SELECT count(*) FROM conversation_messages WHERE chat_id=$1", r.ChatID).
			Scan(&rejectedRows),
	)
	require.Zero(t, rejectedRows)
	p.Permissions["chat.delete"] = true
	_, err = s.RecoverChatMessage(t.Context(), p, r.ChatID, "own", domain.RecoverMessage{Text: "own"})
	require.NoError(t, err)
	require.NoError(t, s.DeleteChat(t.Context(), p, r.ChatID))
	_, err = s.RecoverChatMessage(t.Context(), p, r.ChatID, "own", domain.RecoverMessage{Text: "own"})
	code(t, err, "not_found")
	var count int
	require.NoError(
		t,
		s.Pool().
			QueryRow(t.Context(), "SELECT count(*) FROM conversation_messages WHERE chat_id=$1", r.ChatID).
			Scan(&count),
	)
	require.Zero(t, count)
}

func TestRecoveryCancellationSurvivesRestart(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	p := principal("cancel-handoff")
	r, err := s.CreateRun(t.Context(), p, "create", request())
	require.NoError(t, err)
	r, err = s.ClaimRun(t.Context(), r.ID, "worker", time.Minute)
	require.NoError(t, err)
	step, err := s.BeginStep(t.Context(), r.ID, "worker", r.Fence, "model", json.RawMessage(`{}`), 10)
	require.NoError(t, err)
	in := domain.RecoverMessage{Text: "continue safely", ReplaceRunID: r.ID}
	first, err := s.RecoverChatMessage(t.Context(), p, r.ChatID, "handoff", in)
	require.NoError(t, err)
	require.Equal(t, "recovery_pending", first.Status)
	require.True(t, first.Accepted)
	require.False(t, first.Delivered)
	before, err := s.GetRun(t.Context(), p, r.ID)
	require.NoError(t, err)
	require.True(t, before.CancelRequested)
	replay, err := s.RecoverChatMessage(t.Context(), p, r.ChatID, "handoff", in)
	require.NoError(t, err)
	require.Equal(t, first.MessageID, replay.MessageID)
	after, err := s.GetRun(t.Context(), p, r.ID)
	require.NoError(t, err)
	require.Equal(t, before.Version, after.Version)
	_, err = s.BeginStep(t.Context(), r.ID, "worker", r.Fence, "tool", json.RawMessage(`{}`), 0)
	code(t, err, "stop_requested")
	require.NoError(
		t,
		s.CompleteStep(
			t.Context(),
			r.ID,
			"worker",
			r.Fence,
			step.ID,
			json.RawMessage(`{}`),
			domain.Usage{Microcredits: 2},
		),
	)
	require.NoError(t, s.Transition(t.Context(), r.ID, "worker", r.Fence, domain.Cancelled, "client_cancel", ""))
	s = store.New(s.Pool(), nil)
	next, err := s.RecoverChatMessage(t.Context(), p, r.ChatID, "handoff", in)
	require.NoError(t, err)
	require.Equal(t, first.MessageID, next.MessageID)
	require.True(t, next.Delivered)
	require.NotEqual(t, r.ID, next.RunID)
}

// The user can continue unfinished work any number of times. Each successor
// reads the receipts of a bounded number of linked predecessors.
func TestRecoveryReplacementChainHasNoLimit(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	p := principal("replacement-chain")
	r, err := s.CreateRun(t.Context(), p, "create", request())
	require.NoError(t, err)
	for _, key := range []string{"one", "two", "three", "four", "five"} {
		r, err = s.ClaimRun(t.Context(), r.ID, "worker", time.Minute)
		require.NoError(t, err)
		require.NoError(t, s.Transition(t.Context(), r.ID, "worker", r.Fence, domain.Paused, "budget_exhausted", ""))
		receipt, e := s.RecoverChatMessage(t.Context(), p, r.ChatID, key, domain.RecoverMessage{Text: key})
		require.NoError(t, e)
		require.Equal(t, "queued", receipt.Status)
		previous := r.ID
		r, err = s.GetRun(t.Context(), p, receipt.RunID)
		require.NoError(t, err)
		require.Equal(t, previous, r.ReplacesRunID)
	}
	operations, err := s.RecoveryOperations(t.Context(), r.ID)
	require.NoError(t, err)
	require.Empty(t, operations)
}

func TestRecoveryRevokedSessionRollsBackReplacementButRetainsMessage(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	p := principal("revoked-replacement")
	session, err := s.CreateAuthSession(t.Context(), p, json.RawMessage(`{}`), time.Now().Add(time.Hour))
	require.NoError(t, err)
	in := request()
	in.AuthSessionID = session.ID
	r, err := s.CreateRun(t.Context(), p, "create", in)
	require.NoError(t, err)
	r, err = s.ClaimRun(t.Context(), r.ID, "worker", time.Minute)
	require.NoError(t, err)
	require.NoError(t, s.RevokeAuthSession(t.Context(), p, session.ID))
	receipt, err := s.RecoverChatMessage(
		t.Context(),
		p,
		r.ChatID,
		"revoked",
		domain.RecoverMessage{Text: "continue", ReplaceRunID: r.ID},
	)
	require.NoError(t, err)
	require.Equal(t, "authorization_required", receipt.Reason)
	require.True(t, receipt.Accepted)
	require.False(t, receipt.Delivered)
	previous, err := s.GetRun(t.Context(), p, r.ID)
	require.NoError(t, err)
	require.Equal(t, domain.Running, previous.State)
	require.False(t, previous.CancelRequested)
}

func TestRecoverySteeringCanUseTheRunsExistingReservation(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	p := principal("in-flight-reservation")
	r, err := s.CreateRun(t.Context(), p, "create", request())
	require.NoError(t, err)
	_, err = s.Pool().Exec(t.Context(), "UPDATE billing_accounts SET balance=10 WHERE owner_id=$1", r.OwnerID)
	require.NoError(t, err)
	r, err = s.ClaimRun(t.Context(), r.ID, "worker", time.Minute)
	require.NoError(t, err)
	_, err = s.BeginStep(t.Context(), r.ID, "worker", r.Fence, "model", json.RawMessage(`{}`), 10)
	require.NoError(t, err)
	receipt, err := s.RecoverChatMessage(
		t.Context(),
		p,
		r.ChatID,
		"correction",
		domain.RecoverMessage{Text: "Only evening appointments"},
	)
	require.NoError(t, err)
	require.True(t, receipt.Delivered)
	require.Equal(t, r.ID, receipt.RunID)
	balance, err := s.Balance(t.Context(), p)
	require.NoError(t, err)
	require.Zero(t, balance.AvailableMicrocredits)
	require.EqualValues(t, 10, balance.ReservedMicrocredits)
	require.Zero(t, balance.SpentMicrocredits)
}

func TestRecoveryReportsModelRevocationBeforeCancellation(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	p := principal("model-revoked")
	r, err := s.CreateRun(t.Context(), p, "create", request())
	require.NoError(t, err)
	r, err = s.ClaimRun(t.Context(), r.ID, "worker", time.Minute)
	require.NoError(t, err)
	_, err = s.UpdatePolicy(
		t.Context(),
		p,
		r.OwnerID,
		0,
		domain.UserPolicy{ActiveRunLimit: 8, Models: []domain.Model{}},
	)
	require.NoError(t, err)
	out, err := s.RecoverChatMessage(
		t.Context(),
		p,
		r.ChatID,
		"recover",
		domain.RecoverMessage{Text: "continue", ReplaceRunID: r.ID},
	)
	require.NoError(t, err)
	require.Equal(t, "model_not_allowed", out.Reason)
	require.Equal(t, "select_allowed_model_or_restore_access", out.NextAction)
	require.False(t, out.Delivered)
	after, err := s.GetRun(t.Context(), p, r.ID)
	require.NoError(t, err)
	require.Equal(t, domain.Running, after.State)
	require.False(t, after.CancelRequested)
}
