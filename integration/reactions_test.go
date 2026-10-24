package integration_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
)

func TestReactionProtocolThroughModelAndHTTP(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	input := runRequest("", "", "React to this message")
	input.Interaction.Reactions = &domain.ReactionPolicy{Emoji: []string{"👍"}}
	run := f.createRun(t, input, "reaction-run")
	code, raw := f.request(t, "alice", http.MethodGet, "/v1/chats/"+run.ChatID+"/history", "", nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	initial := decode[struct {
		Events []domain.ChatEvent `json:"events"`
	}](t, raw)
	target := initial.Events[0].Seq
	model, engine := fakeModel(t, f, func(index int) any {
		if index == 1 {
			return map[string]any{
				"id": "reaction-call", "status": "completed",
				"output": []any{map[string]any{
					"type": "function_call", "call_id": "react", "name": "message_react",
					"arguments": fmt.Sprintf(`{"message_seq":%d,"reaction":"👍","active":true}`, target),
				}},
				"usage": map[string]int{"input_tokens": 10, "output_tokens": 5},
			}
		}
		return finalResponse("Acknowledged")
	})
	require.NoError(t, engine.Execute(t.Context(), run.ID))
	require.Equal(t, domain.Completed, f.run(t, run.ID).State)
	code, raw = f.request(t, "alice", http.MethodGet, "/v1/runs/"+run.ID+"/events", "", nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	require.Contains(t, string(raw), "message.reaction")
	require.Contains(t, string(raw), "message_seq")
	model.mu.Lock()
	require.Len(t, model.requests, 2)
	require.Contains(t, string(model.requests[0]), "message_react")
	require.Contains(t, string(model.requests[0]), "message_seq")
	require.Contains(t, string(model.requests[1]), "message.reaction")
	model.mu.Unlock()
	path := "/v1/chats/" + run.ChatID + "/reactions"
	reaction := domain.ReactionChange{MessageSeq: target, Reaction: "👍", Active: true}
	change := domain.SetReaction{SourceEventID: "user-add", ReactionChange: reaction}
	code, raw = f.request(t, "alice", http.MethodPost, path, "", change)
	require.Equal(t, http.StatusCreated, code, string(raw))
	receipt := decode[domain.ChatEvent](t, raw)
	change.SourceEventID, change.Active = "user-remove", false
	code, raw = f.request(t, "alice", http.MethodPost, path, "", change)
	require.Equal(t, http.StatusCreated, code, string(raw))
	change.SourceEventID, change.Active = "user-add", true
	code, raw = f.request(t, "alice", http.MethodPost, path, "", change)
	require.Equal(t, http.StatusCreated, code, string(raw))
	require.Equal(t, receipt.Seq, decode[domain.ChatEvent](t, raw).Seq)
	code, raw = f.request(t, "alice", http.MethodGet, "/v1/chats/"+run.ChatID+"/history", "", nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	history := decode[struct {
		Events []domain.ChatEvent `json:"events"`
	}](t, raw)
	require.Equal(t, map[string][]string{"assistant": {"👍"}}, history.Events[0].Reactions)
	require.Greater(t, history.Events[0].ReactionsSeq, receipt.Seq)
	// A reaction cannot change a completed run or become a conversational answer.
	require.Equal(t, domain.Completed, f.run(t, run.ID).State)
	code, raw = f.request(t, "bob", http.MethodPost, path, "", change)
	require.Equal(t, http.StatusNotFound, code, string(raw))
}
