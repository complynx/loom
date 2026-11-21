package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
)

func TestPassiveContextKeepsHistoryAndLatestActions(t *testing.T) {
	t.Parallel()

	for _, test := range []passiveContextCase{
		{"first dialogue", false, 5, userAction, 0},
		{"after dialogue", true, 5, userAction, 0},
		{"four actions", true, 4, userAction, 0},
		{"large stream", false, 1005, userAction, 0},
		{"external agent", false, 5, domain.ExternalAgentActionEvent, 0},
		{"mixed sources", false, 5, domain.ExternalAgentActionEvent, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			checkPassiveContext(t, test)
		})
	}
}

const userAction = "user.action"

type passiveContextCase struct {
	name    string
	dialog  bool
	actions int
	kind    string
	// userFirst is the number of leading user actions in an external agent case.
	userFirst int
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
		event := domain.AppendEvent{
			SourceEventID: fmt.Sprintf("action-%d", index),
			Type:          test.kind,
			Text:          fmt.Sprintf("action %d", index),
		}
		if index <= test.userFirst {
			event.Type = userAction
		}
		_, err = s.AppendEvent(ctx, p, chat.ID, event)
		if event.Type != userAction {
			require.ErrorContains(t, err, "invalid_event", "only Loom writes external agent actions")
			_, err = s.AppendExternalAction(
				ctx,
				p,
				domain.ExternalAction{ID: event.SourceEventID, ClientID: "client", Tool: "script"},
				event.Text,
			)
		}
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
			if event.Type == test.kind {
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
			if event.Type == test.kind {
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
		if event.Type == test.kind {
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
			require.Equal(t, test.kind != userAction, strings.Contains(summary.Text, "external agent"))
		}
		require.Equal(t, want, kept)
	}
	require.Equal(t, "Help choose", events[len(events)-1].Text)

	return kept, summary
}
