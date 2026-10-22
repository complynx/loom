package runs

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/history"
	"github.com/complynx/loom/internal/provider"
)

func TestCurrentRequestUsesEventIdentityAndKeepsOrder(t *testing.T) {
	t.Parallel()
	run := domain.Run{ID: "current", Input: domain.Input{Text: "Повтори выбор"}}
	events := []domain.ChatEvent{
		{Seq: 1, RunID: "earlier", Type: "message.user", Text: run.Input.Text},
		{Seq: 2, RunID: run.ID, Type: "message.user", Text: run.Input.Text},
		{Seq: 3, Type: "user.action", Text: "Later activity"},
	}
	items, err := history.Build(events, 10, 10000)
	require.NoError(t, err)
	prefix := provider.Message("user", "Untrusted conversation summary")
	items = append([]provider.Item{prefix}, items...)
	out := currentRequestContext(items, events, run)
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	require.Equal(t, 2, strings.Count(string(raw), run.Input.Text))
	require.Contains(t, string(out[2]), "Request provenance: run current, event 2.")
	require.NotContains(t, string(out[1]), "Current explicit request")
	require.Contains(t, string(out[3]), "Later activity")
	require.NotContains(t, string(items[2]), "Current explicit request", "cached prefix must stay neutral")
}

func TestSummaryPrefixStopsBeforeCurrentRequest(t *testing.T) {
	t.Parallel()
	events := []domain.ChatEvent{
		{Seq: 1, RunID: "earlier", Type: "message.user", Text: "Earlier"},
		{Seq: 2, RunID: "current", Type: "message.user", Text: "Current"},
		{Seq: 3, Type: "user.action", Text: "Later activity"},
		{Seq: 4, Type: "user.action", Text: "Latest activity"},
	}
	prefix, err := summaryPrefix(events, 1, contextBudget{trigger: 10000, target: 10000}, "current")
	require.NoError(t, err)
	require.Equal(t, events[:1], prefix)
	_, err = summaryPrefix(events[1:], 1, contextBudget{trigger: 10000, target: 10000}, "current")
	require.ErrorIs(t, err, history.ErrContextUnavailable)
}
