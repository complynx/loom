package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/clx-scripter/internal/domain"
	"github.com/complynx/clx-scripter/internal/provider"
	"github.com/complynx/clx-scripter/internal/runs"
)

func TestPersonalMicroMemoryReloadsWithinRunAndPreviousRun(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://boundary", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer alice")
	p, err := testAuth{}.Authenticate(t.Context(), req)
	require.NoError(t, err)
	micro, err := f.repository.SetMemoryMicro(t.Context(), p, domain.MemoryWrite{Text: "prefers mornings"})
	require.NoError(t, err)
	model, _ := fakeModel(t, f, func(index int) any {
		if index == 1 {
			return map[string]any{
				"id":     "memory-call",
				"status": "completed",
				"output": []any{
					map[string]any{
						"type":      "function_call",
						"call_id":   "memory-update",
						"name":      "script",
						"arguments": `{"code":"update memory"}`,
					},
				},
				"usage": map[string]int{"input_tokens": 10, "output_tokens": 5},
			}
		}
		return finalResponse("done")
	})
	client, err := provider.NewResponses(model.server.URL, "test-provider-key", model.server.Client())
	require.NoError(t, err)
	engine := runs.New(f.repository, client, runs.Options{Owner: "memory-worker",
		Authorize: func(context.Context, domain.Run) (domain.Principal, string, error) { return p, "", nil },
		Script: func(ctx context.Context, _ domain.Run, _ domain.Principal, _, _ string) (json.RawMessage, error) {
			_, writeErr := f.repository.SetMemoryMicro(
				ctx,
				p,
				domain.MemoryWrite{Text: "prefers evenings", Version: micro.Version},
			)
			return json.RawMessage(`{"updated":true}`), writeErr
		}})
	run := f.createRun(t, runRequest("", "", "assist"), "micro-live")
	require.NoError(t, engine.Execute(t.Context(), run.ID))
	require.Equal(t, domain.Completed, f.run(t, run.ID).State)
	model.mu.Lock()
	require.Len(t, model.requests, 2)
	require.Contains(t, string(model.requests[0]), "prefers mornings")
	require.Contains(t, string(model.requests[1]), "prefers evenings")
	require.NotContains(t, string(model.requests[1]), "prefers mornings")
	model.mu.Unlock()
	state, err := f.repository.MemoryState(t.Context(), p)
	require.NoError(t, err)
	require.NoError(t, f.repository.ClearUserMemory(t.Context(), p, state.Revision))
	request := runRequest("", run.ChatID, "continue")
	request.PreviousRunID = run.ID
	next := f.createRun(t, request, "micro-cleared")
	require.NoError(t, engine.Execute(t.Context(), next.ID))
	model.mu.Lock()
	require.Len(t, model.requests, 3)
	require.NotContains(t, string(model.requests[2]), "prefers evenings")
	require.NotContains(t, string(model.requests[2]), "prefers mornings")
	model.mu.Unlock()
	_, err = f.repository.SetMemoryMicro(
		t.Context(),
		p,
		domain.MemoryWrite{Text: "private preference", Version: state.Micro.Version + 1},
	)
	require.NoError(t, err)
	denied := f.createRun(t, runRequest("", "", "memory without extra roles"), "micro-no-right")
	require.NoError(t, engine.Execute(t.Context(), denied.ID))
	model.mu.Lock()
	require.Len(t, model.requests, 4)
	require.Contains(t, string(model.requests[3]), "private preference")
	model.mu.Unlock()
}
