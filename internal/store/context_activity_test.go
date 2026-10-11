package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
)

func TestPassiveContextKeepsHistoryAndLatestActions(t *testing.T) {
	t.Parallel()

	for _, test := range []passiveContextCase{{"first dialogue", false, 5}, {"after dialogue", true, 5}, {"four actions", true, 4}, {"large stream", false, 1005}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			checkPassiveContext(t, test)
		})
	}
}

type passiveContextCase struct {
	name    string
	dialog  bool
	actions int
}

func checkPassiveContext(t *testing.T, test passiveContextCase) {
	t.Helper()

	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	chat, err := s.DefaultChat(ctx, p)
	require.NoError(t, err)
	if test.dialog {
		previous, previousErr := s.CreateRun(ctx, p, "previous", request())
		require.NoError(t, previousErr)
		previous, previousErr = s.ClaimRun(ctx, previous.ID, "worker", time.Minute)
		require.NoError(t, previousErr)
		require.NoError(
			t,
			s.Transition(ctx, previous.ID, "worker", previous.Fence, domain.Completed, "", "previous answer"),
		)
	}
	for index := 1; index <= test.actions; index++ {
		_, err = s.AppendEvent(
			ctx,
			p,
			chat.ID,
			domain.AppendEvent{
				SourceEventID: fmt.Sprintf("action-%d", index),
				Type:          "user.action",
				Text:          fmt.Sprintf("action %d", index),
			},
		)
		require.NoError(t, err)
	}
	input := request()
	input.Input.Text = "Help choose"
	run, err := s.CreateRun(ctx, p, "current", input)
	require.NoError(t, err)
	events, err := s.ExecutionContextHistoryAfter(ctx, run.ID, 0, 1001)
	require.NoError(t, err)
	kept, summary := assertPassiveContext(t, test, events)
	if summary != nil {
		var provenance struct {
			FirstSeq int64 `json:"first_seq"`
		}
		require.NoError(t, json.Unmarshal(summary.Data, &provenance))
		partial, checkpointErr := s.ExecutionContextHistoryAfter(ctx, run.ID, provenance.FirstSeq, 1001)
		require.NoError(t, checkpointErr)
		var partialKept []string
		for _, event := range partial {
			if event.Type == "user.action" {
				partialKept = append(partialKept, event.Text)
			}
		}
		require.Equal(
			t,
			kept[len(kept)-3:],
			partialKept,
			"checkpoint inside hidden block must not expand remaining actions",
		)
	}
	// Model-view compression must never remove the service event from search.
	results, err := s.Search(
		ctx,
		p,
		chat.ID,
		domain.SearchRequest{Mode: "regex", Query: "^action 2$", Limit: 10},
	)
	require.NoError(t, err)
	require.Len(t, results.Events, 1)
	require.Equal(t, "action 2", results.Events[0].Text)
	if test.actions > 4 {
		claimed, nextErr := s.ClaimRun(ctx, run.ID, "worker", time.Minute)
		require.NoError(t, nextErr)
		require.NoError(t, s.Transition(ctx, claimed.ID, "worker", claimed.Fence, domain.Completed, "", "done"))
		next, nextErr := s.CreateRun(ctx, p, "next", input)
		require.NoError(t, nextErr)
		nextView, nextErr := s.ExecutionContextHistoryAfter(ctx, next.ID, 0, 1001)
		require.NoError(t, nextErr)
		var nextKept []string
		for _, event := range nextView {
			if event.Type == "user.action" {
				nextKept = append(nextKept, event.Text)
			}
		}
		require.Equal(t, kept, nextKept, "old activity must not expand on the next run")
	}
}

func assertPassiveContext(
	t *testing.T,
	test passiveContextCase,
	events []domain.ChatEvent,
) ([]string, *domain.ChatEvent) {
	t.Helper()
	var kept []string
	var summary *domain.ChatEvent
	for index := range events {
		event := &events[index]
		if event.Type == "user.action" {
			kept = append(kept, event.Text)
		}
		if event.Type == "user.activity_summary" {
			summary = event
		}
	}
	if test.actions <= 4 {
		require.Nil(t, summary)
		require.Equal(t, []string{"action 1", "action 2", "action 3", "action 4"}, kept)
	} else {
		require.NotNil(t, summary)
		want := []string{
			fmt.Sprintf("action %d", test.actions-2),
			fmt.Sprintf("action %d", test.actions-1),
			fmt.Sprintf("action %d", test.actions),
		}
		if test.dialog {
			want = append([]string{"action 1"}, want...)
			require.Contains(t, summary.Text, "1 more actions")
		} else {
			require.Contains(t, summary.Text, fmt.Sprintf("%d actions before first request", test.actions))
		}
		require.Equal(t, want, kept)
	}
	require.Equal(t, "Help choose", events[len(events)-1].Text)

	return kept, summary
}
