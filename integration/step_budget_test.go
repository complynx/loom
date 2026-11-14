package integration_test

import (
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
	model, engine := fakeModel(t, f, func(n int) any {
		if n < 4 {
			return finalResponse(`<tg-emoji emoji-id="12">😀</tg-emoji>`)
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
	require.Len(t, model.requests, 4)
	require.Contains(t, string(model.requests[3]), "Continue")
}
