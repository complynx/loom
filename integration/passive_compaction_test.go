package integration_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
)

func TestPassiveBlockCompactionAdvancesOnceAndPreservesInterleavedFacts(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	settings, err := f.repository.GetSettings(ctx)
	require.NoError(t, err)
	settings.HistoryRecentLimit = 4
	admin := domain.Principal{
		Issuer:      "https://test.identity",
		Subject:     "admin",
		OrgID:       "test-org",
		Permissions: map[string]bool{"settings.write": true},
	}
	_, err = f.repository.UpdateSettings(ctx, admin, settings.Version, settings)
	require.NoError(t, err)
	p := domain.Principal{
		Issuer:      "https://test.identity",
		Subject:     "alice",
		Actor:       "alice",
		OrgID:       "test-org",
		ExpiresAt:   time.Now().Add(time.Hour),
		Permissions: map[string]bool{"chat.read": true, "chat.events.append": true},
	}
	for index := 1; index <= 100; index++ {
		_, err = f.repository.AppendEvent(
			ctx,
			p,
			"default",
			domain.AppendEvent{
				SourceEventID: fmt.Sprintf("activity-%d", index),
				Type:          "user.action",
				Text:          fmt.Sprintf("action %d", index),
			},
		)
		require.NoError(t, err)
		if index == 50 {
			_, err = f.repository.AppendEvent(
				ctx,
				p,
				"default",
				domain.AppendEvent{SourceEventID: "fact", Type: "service.notice", Text: "Preserve interleaved fact"},
			)
			require.NoError(t, err)
		}
	}
	model, engine := fakeModel(t, f, func(int) any { return finalResponse("done") })
	run := f.createRun(t, runRequest("", "", "Help choose"), "passive-summary")
	require.NoError(t, engine.Execute(ctx, run.ID))
	require.Equal(t, domain.Completed, f.run(t, run.ID).State)
	steps, err := f.repository.ExecutionSteps(ctx, run.ID)
	require.NoError(t, err)
	summaries := 0
	for _, step := range steps {
		if step.Kind == "summary" {
			summaries++
		}
	}
	require.Equal(t, 1, summaries, "placeholder coverage must advance to the last hidden action")
	model.mu.Lock()
	require.Contains(t, string(model.requests[0]), "Preserve interleaved fact")
	require.Contains(t, string(model.requests[0]), "100 actions before first request")
	last := string(model.requests[len(model.requests)-1])
	model.mu.Unlock()
	require.True(t, strings.Contains(last, "action 98") && strings.Contains(last, "action 100"))
}
