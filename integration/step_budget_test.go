package integration_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/store"
)

func TestInputRenewsStepBudgetAcrossRestart(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.pool.Exec(t.Context(), `UPDATE settings SET doc=jsonb_set(doc,'{max_steps}','3')`)
	require.NoError(t, err)
	input := runRequest("", "", "Reply")
	input.OutputPolicy = &domain.OutputPolicy{DisallowCustomEmoji: true}
	r := f.createRun(t, input, "budget")
	const note = "Two replies were rejected. Continue?"
	model, engine := fakeModel(t, f, func(n int) any {
		switch {
		case n < 4:
			return finalResponse(`<tg-emoji emoji-id="12">😀</tg-emoji>`)
		case n == 4:
			// The run reached its step limit: the model writes a progress note.
			return finalResponse(note)
		}
		return finalResponse("Done")
	})
	require.NoError(t, engine.Execute(t.Context(), r.ID))
	require.Equal(t, "step_limit", f.run(t, r.ID).Reason)

	sendConversationText(t, f, r.ID, "continue-1", "Try again")
	require.Equal(t, domain.Queued, f.run(t, r.ID).State)
	// A new store and engine must recover both the history and the renewed budget.
	engine = validationEngine(t, f, model, store.New(f.pool, nil), nil)
	require.NoError(t, engine.Execute(t.Context(), r.ID))
	paused := f.run(t, r.ID)
	require.Equal(t, domain.Paused, paused.State)
	require.Equal(t, "step_limit", paused.Reason)
	require.Equal(t, 3, paused.StepBudgetStart)
	// The pause carries the note as an assistant message, not as a final answer.
	require.Equal(t, note, paused.Result)
	var progress, final int
	require.NoError(
		t,
		f.pool.QueryRow(
			t.Context(),
			"SELECT count(*) FILTER(WHERE type='message.progress'),count(*) FILTER(WHERE type='message.completed') FROM run_events WHERE run_id=$1",
			r.ID,
		).Scan(&progress, &final),
	)
	require.Equal(t, 1, progress)
	require.Zero(t, final)

	// Replaying the same request must neither wake the run nor grant more steps.
	sendConversationText(t, f, r.ID, "continue-1", "Try again")
	replayed := f.run(t, r.ID)
	require.Equal(t, domain.Paused, replayed.State)
	require.Equal(t, paused.StepBudgetStart, replayed.StepBudgetStart)

	sendConversationText(t, f, r.ID, "continue-2", "Continue")
	require.NoError(t, engine.Execute(t.Context(), r.ID))
	done := f.run(t, r.ID)
	require.Equal(t, domain.Completed, done.State)
	require.Equal(t, "Done", done.Result)
	model.mu.Lock()
	defer model.mu.Unlock()
	require.Len(t, model.requests, 5)
	require.Contains(t, string(model.requests[3]), "work allowance for this request is used")
	// The continued run sees its own note and the reply of the user.
	require.Contains(t, string(model.requests[4]), note)
	require.Contains(t, string(model.requests[4]), "Continue")
}

// A run that spends its own allowance, while the wallet has funds, reports its
// progress and offers to continue. The reply of the user continues the work in a
// linked successor.
func TestSpentRunAllowanceWritesNoteAndOffersToContinue(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	input := runRequest("", "", "Reply")
	input.OutputPolicy = &domain.OutputPolicy{DisallowCustomEmoji: true}
	r := f.createRun(t, input, "allowance")
	const note = "One reply was rejected. Continue?"
	model, engine := fakeModel(t, f, func(n int) any {
		switch n {
		case 1:
			// The allowance ends with this request: it is just below the current
			// reservation, so no second request fits. The output policy rejects the
			// reply, so the run needs one more request.
			if _, err := f.pool.Exec(
				context.Background(),
				"UPDATE runs SET doc=jsonb_set(doc,'{max_microcredits}',to_jsonb(reserved_microcredits-1)) WHERE id=$1",
				r.ID,
			); err != nil {
				t.Error(err)
			}
			return finalResponse(`<tg-emoji emoji-id="12">😀</tg-emoji>`)
		case 2:
			return finalResponse(note)
		}
		return finalResponse("Done")
	})
	require.NoError(t, engine.Execute(t.Context(), r.ID))
	paused := f.run(t, r.ID)
	require.Equal(t, domain.Paused, paused.State)
	require.Equal(t, "budget_exhausted", paused.Reason)
	require.Equal(t, note, paused.Result)
	path := "/v1/chats/" + r.ChatID
	code, raw := f.request(t, "alice", "GET", path+"/active-run", "", nil)
	require.Equal(t, 200, code, string(raw))
	require.Equal(t, "ask_user_to_continue", decode[domain.RecoveryReceipt](t, raw).NextAction)
	code, raw = f.request(
		t,
		"alice",
		"POST",
		path+"/messages",
		"continue",
		domain.RecoverMessage{Text: "Yes, continue"},
	)
	require.Equal(t, 202, code, string(raw))
	receipt := decode[domain.RecoveryReceipt](t, raw)
	require.True(t, receipt.Delivered)
	require.NotEqual(t, r.ID, receipt.RunID)
	require.NoError(t, engine.Execute(t.Context(), receipt.RunID))
	successor := f.run(t, receipt.RunID)
	require.Equal(t, domain.Completed, successor.State)
	require.Equal(t, r.ID, successor.ReplacesRunID)
	model.mu.Lock()
	defer model.mu.Unlock()
	require.Len(t, model.requests, 3)
	// The successor sees the note of its predecessor and the reply of the user.
	require.Contains(t, string(model.requests[2]), note)
	require.Contains(t, string(model.requests[2]), "Yes, continue")
}
