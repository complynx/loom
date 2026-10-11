package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/clx-scripter/internal/domain"
	"github.com/complynx/clx-scripter/internal/provider"
	"github.com/complynx/clx-scripter/internal/runs"
)

func TestPaidHistorySummaryPreservesConstraintsAcrossQuestionResume(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	settings, err := f.repository.GetSettings(context.Background())
	require.NoError(t, err)
	settings.HistoryRecentLimit = 2
	_, err = f.repository.UpdateSettings(
		context.Background(),
		domain.Principal{Actor: "qa-admin", Permissions: map[string]bool{"settings.write": true}},
		settings.Version,
		settings,
	)
	require.NoError(t, err)
	code, raw := f.request(t, "alice", "GET", "/v1/chats/default", "", nil)
	require.Equal(t, 200, code, string(raw))
	chat := decode[domain.Chat](t, raw)
	for i, text := range []string{"Constraint: book only after 18:00; specialist Andrey; user-source event choice-42", "Opened massage menu", "Viewed dates", "Viewed available slots"} {
		code, raw = f.request(
			t,
			"alice",
			"POST",
			"/v1/chats/"+chat.ID+"/events",
			"",
			domain.AppendEvent{
				SourceEventID: fmt.Sprintf("history-%d", i),
				Type:          "user.action",
				Action:        "navigation",
				Text:          text,
				OccurredAt:    time.Now(),
			},
		)
		require.Equal(t, 201, code, string(raw))
	}
	model, engine := fakeModel(t, f, func(index int) any {
		switch index {
		case 1:
			return finalResponse(
				"Preserved constraint from event 1: only after 18:00, specialist Andrey, choice-42. Events 2 and 3 viewed menu and dates.",
			)
		case 2:
			return map[string]any{
				"id":     "provider-after-summary-question",
				"status": "completed",
				"output": []any{
					map[string]any{
						"type":      "function_call",
						"call_id":   "date-choice",
						"name":      "question",
						"arguments": `{"text":"Which date?","choices":[{"id":"monday","label":"Monday"}],"allow_text":false}`,
					},
				},
				"usage": map[string]int{"input_tokens": 10, "output_tokens": 5},
			}
		default:
			return finalResponse("Monday after 18:00 with Andrey")
		}
	})
	run := f.createRun(t, runRequest("", "", "Find an appointment on my preferred schedule"), "summary-run")
	require.NoError(t, engine.Execute(context.Background(), run.ID))
	waiting := f.run(t, run.ID)
	require.Equal(t, domain.WaitingInput, waiting.State)
	require.EqualValues(t, 30, waiting.SpentMicrocredits)
	code, raw = f.request(
		t,
		"alice",
		"POST",
		"/v1/runs/"+run.ID+"/input",
		"date-answer",
		domain.RunInput{Kind: "answer", QuestionID: waiting.Question.ID, ChoiceIDs: []string{"monday"}},
	)
	require.Equal(t, 200, code, string(raw))
	client, err := provider.NewResponses(model.server.URL, "test-provider-key", model.server.Client())
	require.NoError(t, err)
	restart := runs.New(
		f.repository,
		client,
		runs.Options{
			Owner: "summary-restart",
			Authorize: func(context.Context, domain.Run) (domain.Principal, string, error) {
				return domain.Principal{
					Subject:     "alice",
					Permissions: map[string]bool{"chat.read": true, "runs.create": true},
				}, "token", nil
			},
		},
	)
	require.NoError(t, restart.Execute(context.Background(), run.ID))
	final := f.run(t, run.ID)
	require.Equal(t, domain.Completed, final.State)
	require.EqualValues(t, 45, final.SpentMicrocredits)
	require.Zero(t, final.ReservedMicrocredits)
	model.mu.Lock()
	defer model.mu.Unlock()
	require.Len(t, model.requests, 3)
	var summaryRequest map[string]any
	require.NoError(t, json.Unmarshal(model.requests[0], &summaryRequest))
	require.Contains(t, summaryRequest["instructions"], "Summarize")
	require.NotContains(t, summaryRequest, "tools")
	require.Contains(t, string(model.requests[0]), "choice-42")
	for _, request := range model.requests[1:] {
		require.Contains(t, string(request), "only after 18:00")
		require.Contains(t, string(request), "Current explicit request: Find an appointment on my preferred schedule")
		require.Contains(t, string(request), "Untrusted conversation summary")
	}
	require.Contains(t, string(model.requests[2]), "monday")
}
