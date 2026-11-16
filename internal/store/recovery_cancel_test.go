package store_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
)

func TestExplicitCancelWithdrawsPendingRecovery(t *testing.T) {
	t.Parallel()
	for _, stopAfterTransition := range []bool{false, true} {
		t.Run(
			map[bool]string{false: "stop-before-terminal", true: "stop-after-terminal"}[stopAfterTransition],
			func(t *testing.T) {
				t.Parallel()
				testExplicitCancelWithdrawsPendingRecovery(t, stopAfterTransition)
			},
		)
	}
}

func testExplicitCancelWithdrawsPendingRecovery(t *testing.T, stopAfterTransition bool) {
	t.Helper()
	s := testStore(t)
	p := principal("stop-recovery")
	r, err := s.CreateRun(t.Context(), p, "create", request())
	require.NoError(t, err)
	r, err = s.ClaimRun(t.Context(), r.ID, "worker", time.Minute)
	require.NoError(t, err)
	in := domain.RecoverMessage{Text: "Continue", ReplaceRunID: r.ID}
	first, err := s.RecoverChatMessage(t.Context(), p, r.ChatID, "recover", in)
	require.NoError(t, err)
	require.Equal(t, "cancellation_pending", first.Reason)
	if stopAfterTransition {
		require.NoError(t, s.Transition(t.Context(), r.ID, "worker", r.Fence, domain.Cancelled, "client_cancel", ""))
		r, err = s.GetRun(t.Context(), p, r.ID)
		require.NoError(t, err)
	}
	controlled, err := s.Control(t.Context(), p, r.ID, "explicit-stop", "cancel")
	require.NoError(t, err)
	if stopAfterTransition {
		require.Equal(t, r.Version, controlled.Version, "terminal stop must not append a new status event")
	}
	if !stopAfterTransition {
		located, locateErr := s.ActiveChatRun(t.Context(), p, r.ChatID)
		require.NoError(t, locateErr)
		require.Equal(t, "cancelling", located.Status)
		require.Equal(t, "read_run", located.NextAction)
		require.NoError(t, s.Transition(t.Context(), r.ID, "worker", r.Fence, domain.Cancelled, "client_cancel", ""))
	}
	retry, err := s.RecoverChatMessage(t.Context(), p, r.ChatID, "recover", in)
	require.NoError(t, err)
	require.True(t, retry.Abandoned)
	require.False(t, retry.Delivered)
	stopped, err := s.GetRun(t.Context(), p, r.ID)
	require.NoError(t, err)
	require.Equal(t, domain.Cancelled, stopped.State)
	require.Equal(t, "client_cancel", stopped.Reason)
	var count int
	require.NoError(
		t,
		s.Pool().QueryRow(t.Context(), "SELECT count(*) FROM runs WHERE chat_id=$1", r.ChatID).Scan(&count),
	)
	require.Equal(t, 1, count)
}

func TestCancelOldRunPreservesLaterWork(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	p := principal("stop-old-run")
	r, err := s.CreateRun(t.Context(), p, "create", request())
	require.NoError(t, err)
	_, err = s.Control(t.Context(), p, r.ID, "stop", "cancel")
	require.NoError(t, err)
	// A terminal run's ordinary new round is not a continuation of that run.
	_, err = s.Pool().Exec(t.Context(), "UPDATE billing_accounts SET balance=0 WHERE owner_id=$1", r.OwnerID)
	require.NoError(t, err)
	in := domain.RecoverMessage{Text: "A distinct request"}
	first, err := s.RecoverChatMessage(t.Context(), p, r.ChatID, "new-round", in)
	require.NoError(t, err)
	require.False(t, first.Delivered)
	_, err = s.Control(t.Context(), p, r.ID, "stop-again", "cancel")
	require.NoError(t, err)
	retry, err := s.RecoverChatMessage(t.Context(), p, r.ChatID, "new-round", in)
	require.NoError(t, err)
	require.False(t, retry.Abandoned)
	_, err = s.Pool().Exec(t.Context(), "UPDATE billing_accounts SET balance=1000000 WHERE owner_id=$1", r.OwnerID)
	require.NoError(t, err)
	retry, err = s.RecoverChatMessage(t.Context(), p, r.ChatID, "new-round", in)
	require.NoError(t, err)
	require.True(t, retry.Delivered)
	later, err := s.ClaimRun(t.Context(), retry.RunID, "worker", time.Minute)
	require.NoError(t, err)
	continuation := domain.RecoverMessage{Text: "Continue new work", ReplaceRunID: later.ID}
	_, err = s.RecoverChatMessage(t.Context(), p, r.ChatID, "later-continuation", continuation)
	require.NoError(t, err)
	_, err = s.Control(t.Context(), p, r.ID, "old-stop-again", "cancel")
	require.NoError(t, err)
	retry, err = s.RecoverChatMessage(t.Context(), p, r.ChatID, "later-continuation", continuation)
	require.NoError(t, err)
	require.False(t, retry.Abandoned)
	require.Equal(t, "cancellation_pending", retry.Reason)
}

func TestCancelSuccessorWithdrawsQueuedContinuations(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	p := principal("stop-successor")
	r, err := s.CreateRun(t.Context(), p, "create", request())
	require.NoError(t, err)
	r, err = s.ClaimRun(t.Context(), r.ID, "worker", time.Minute)
	require.NoError(t, err)
	handoff := domain.RecoverMessage{Text: "Continue", ReplaceRunID: r.ID}
	_, err = s.RecoverChatMessage(t.Context(), p, r.ChatID, "handoff", handoff)
	require.NoError(t, err)
	later := domain.RecoverMessage{Text: "Also check this"}
	queued, err := s.RecoverChatMessage(t.Context(), p, r.ChatID, "later", later)
	require.NoError(t, err)
	require.Equal(t, "earlier_message_pending", queued.Reason)
	require.NoError(t, s.Transition(t.Context(), r.ID, "worker", r.Fence, domain.Cancelled, "client_cancel", ""))
	next, err := s.RecoverChatMessage(t.Context(), p, r.ChatID, "handoff", handoff)
	require.NoError(t, err)
	require.True(t, next.Delivered)
	_, err = s.Control(t.Context(), p, next.RunID, "stop-successor", "cancel")
	require.NoError(t, err)
	retry, err := s.RecoverChatMessage(t.Context(), p, r.ChatID, "later", later)
	require.NoError(t, err)
	require.True(t, retry.Abandoned)
	require.False(t, retry.Delivered)
}

func TestRecoveryRejectsNULBeforeAcceptance(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	p := principal("nul-message")
	r, err := s.CreateRun(t.Context(), p, "create", request())
	require.NoError(t, err)
	_, err = s.RecoverChatMessage(
		t.Context(),
		p,
		r.ChatID,
		"nul",
		domain.RecoverMessage{Text: string([]byte{'a', 0, 'b'})},
	)
	code(t, err, "invalid_message")
}

func TestRecoveryDoesNotReplaceLegacyRunWithoutAdmissionOrder(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	p := principal("legacy-active-order")
	r, err := s.CreateRun(t.Context(), p, "create", request())
	require.NoError(t, err)
	_, err = s.Pool().
		Exec(t.Context(), "UPDATE runs SET doc=doc-'admission_sequence',state='paused',reason='budget_exhausted' WHERE id=$1", r.ID)
	require.NoError(t, err)
	located, err := s.ActiveChatRun(t.Context(), p, r.ChatID)
	require.NoError(t, err)
	require.Equal(t, "run_order_unavailable", located.Reason)
	require.Equal(t, "stop_run_then_create_run", located.NextAction)
	// A second blocker must not change the precedence between the two routes.
	_, err = s.Pool().Exec(t.Context(), "UPDATE billing_accounts SET balance=0 WHERE owner_id=$1", r.OwnerID)
	require.NoError(t, err)
	located, err = s.ActiveChatRun(t.Context(), p, r.ChatID)
	require.NoError(t, err)
	require.Equal(t, "run_order_unavailable", located.Reason)
	receipt, err := s.RecoverChatMessage(
		t.Context(),
		p,
		r.ChatID,
		"replace",
		domain.RecoverMessage{Text: "Continue", ReplaceRunID: r.ID},
	)
	require.NoError(t, err)
	require.False(t, receipt.Delivered)
	require.Equal(t, "run_order_unavailable", receipt.Reason)
	require.Equal(t, located.NextAction, receipt.NextAction)
	current, err := s.GetRun(t.Context(), p, r.ID)
	require.NoError(t, err)
	require.False(t, current.CancelRequested)
	require.Equal(t, domain.Paused, current.State)
}

func TestRecoveryLocatorRejectsOversizedHandoff(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	p := principal("oversized-handoff")
	r, err := s.CreateRun(t.Context(), p, "create", request())
	require.NoError(t, err)
	_, err = s.Pool().Exec(t.Context(), `INSERT INTO write_operations(id,run_id,call_id,state,doc)
		SELECT gen_random_uuid(),$1,'write-'||n,'succeeded','{}' FROM generate_series(1,65) n`, r.ID)
	require.NoError(t, err)
	_, err = s.Pool().Exec(t.Context(), "UPDATE runs SET state='paused',reason='budget_exhausted' WHERE id=$1", r.ID)
	require.NoError(t, err)
	located, err := s.ActiveChatRun(t.Context(), p, r.ChatID)
	require.NoError(t, err)
	require.Equal(t, "recovery_context_too_large", located.Reason)
	receipt, err := s.RecoverChatMessage(t.Context(), p, r.ChatID, "continue", domain.RecoverMessage{Text: "Continue"})
	require.NoError(t, err)
	require.False(t, receipt.Delivered)
	require.Equal(t, located.Reason, receipt.Reason)
	require.Equal(t, located.NextAction, receipt.NextAction)
}
