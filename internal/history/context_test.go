package history_test

import (
	"errors"
	"testing"

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
