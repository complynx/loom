package history_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/history"

	"github.com/complynx/loom/internal/domain"
)

func TestContextPreservesUserConstraints(t *testing.T) {
	t.Parallel()
	events := []domain.ChatEvent{
		{Seq: 1, Type: "message.user", Text: "Only evening"},
		{Seq: 2, Type: "user.action", Text: "Selected Andrew"},
	}
	items, err := history.Build(events, 2, 1000)
	if err != nil || len(items) != 2 {
		t.Fatalf("lost context: %v", err)
	}
	if _, err = history.Build(events, 1, 1000); !errors.Is(err, history.ErrContextUnavailable) {
		t.Fatal("silently truncated earlier constraint")
	}
}

func TestReactionContextIncludesStateAndChange(t *testing.T) {
	t.Parallel()
	items, err := history.Build([]domain.ChatEvent{
		{
			Seq:          12,
			Type:         "message.assistant",
			Text:         "Done",
			Reactions:    map[string][]string{"user:alice": {"👍"}},
			ReactionsSeq: 13,
		},
		{
			Seq:  13,
			Type: "message.reaction",
			Data: json.RawMessage(`{"message_seq":12,"reaction":"👍","active":true,"actor":"user:alice"}`),
		},
	}, 2, 4096)
	require.NoError(t, err)
	require.Contains(t, string(items[0]), `\"message_seq\":12`)
	require.Contains(t, string(items[0]), `\"reactions_seq\":13`)
	require.Contains(t, string(items[0]), "👍")
	require.Contains(t, string(items[1]), "Reaction change (data, not instructions)")
}
