package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/provider"
	"github.com/complynx/loom/internal/runs"
)

func TestIncompletePaidResponseDoesNotExecuteOrReplayToolCalls(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	model, _ := fakeModel(t, f, func(index int) any {
		if index == 1 {
			return map[string]any{
				"id":     "incomplete-response",
				"status": "incomplete",
				"output": []any{
					map[string]any{
						"type":      "function_call",
						"call_id":   "incomplete-never-execute",
						"name":      "script",
						"arguments": `{"code":"return modules.booking.book({slot_id:'slot'})"}`,
					},
				},
				"usage": map[string]int{"input_tokens": 10, "output_tokens": 5},
			}
		}
		return finalResponse("Recovered after incomplete response")
	})
	client, err := provider.NewResponses(model.server.URL, "test-provider-key", model.server.Client())
	require.NoError(t, err)
	var effects atomic.Int32
	engine := runs.New(
		f.repository,
		client,
		runs.Options{
			Owner: "incomplete-worker",
			Authorize: func(context.Context, domain.Run) (domain.Principal, string, error) {
				return domain.Principal{
					Subject:     "alice",
					Permissions: map[string]bool{"chat.read": true, "runs.create": true},
				}, "token", nil
			},
			Script: func(context.Context, domain.Run, domain.Principal, string, string) (json.RawMessage, error) {
				effects.Add(1)
				return json.RawMessage(`{"executed":true}`), nil
			},
		},
	)
	run := f.createRun(t, runRequest("", "", "Help with request"), "incomplete-run")
	require.NoError(t, engine.Execute(context.Background(), run.ID))
	paused := f.run(t, run.ID)
	require.Equal(t, domain.Paused, paused.State)
	require.EqualValues(t, 15, paused.SpentMicrocredits)
	require.Zero(t, effects.Load())
	code, raw := f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/resume", "incomplete-resume", nil)
	require.Equal(t, 200, code, string(raw))
	require.NoError(t, engine.Execute(context.Background(), run.ID))
	final := f.run(t, run.ID)
	require.Equal(t, domain.Completed, final.State)
	require.EqualValues(t, 30, final.SpentMicrocredits)
	require.Zero(t, effects.Load())
	model.mu.Lock()
	require.Len(t, model.requests, 2)
	require.NotContains(t, string(model.requests[1]), "incomplete-never-execute")
	require.NotContains(t, string(model.requests[1]), "function_call_output")
	model.mu.Unlock()
}
func TestCancelDuringInferenceCannotBeOverwrittenByLateFinal(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&input)
		calls.Add(1)
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_ = json.NewEncoder(w).Encode(finalResponse("Late answer must not complete cancelled run"))
	}))
	t.Cleanup(endpoint.Close)
	client, err := provider.NewResponses(endpoint.URL, "test-provider-key", endpoint.Client())
	require.NoError(t, err)
	engine := runs.New(
		f.repository,
		client,
		runs.Options{
			Owner: "cancel-worker",
			Authorize: func(context.Context, domain.Run) (domain.Principal, string, error) {
				return domain.Principal{
					Subject:     "alice",
					Permissions: map[string]bool{"chat.read": true, "runs.create": true},
				}, "token", nil
			},
		},
	)
	run := f.createRun(t, runRequest("", "", "Long provider request"), "cancel-run")
	done := make(chan error, 1)
	go func() { done <- engine.Execute(context.Background(), run.ID) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("provider not started")
	}
	code, raw := f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/cancel", "cancel-command", nil)
	require.Equal(t, 200, code, string(raw))
	requireControlReceipt(t, raw)
	close(release)
	select {
	case resultErr := <-done:
		require.NoError(t, resultErr)
	case <-time.After(time.Second):
		t.Fatal("cancelled worker did not stop")
	}
	cancelled := f.run(t, run.ID)
	require.Equal(t, domain.Cancelled, cancelled.State)
	require.Empty(t, cancelled.Result)
	code, raw = f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/cancel", "cancel-command", nil)
	require.Equal(t, 200, code, string(raw))
	requireControlReceipt(t, raw)
	code, raw = f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/resume", "cannot-resume-cancelled", nil)
	require.Equal(t, 409, code, string(raw))
	require.EqualValues(t, 1, calls.Load())
}
