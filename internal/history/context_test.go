package history

import (
	"errors"
	"github.com/complynx/clx-scripter/internal/domain"
	"testing"
)

func TestContextPreservesUserConstraints(t *testing.T) {
	events := []domain.ChatEvent{{Seq: 1, Type: "message.user", Text: "Only evening"}, {Seq: 2, Type: "user.action", Text: "Selected Andrew"}}
	items, err := Build(events, 2, 1000)
	if err != nil || len(items) != 2 {
		t.Fatalf("lost context: %v", err)
	}
	if _, err = Build(events, 1, 1000); !errors.Is(err, ErrContextUnavailable) {
		t.Fatal("silently truncated earlier constraint")
	}
}
