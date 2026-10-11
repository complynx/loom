package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/clx-scripter/internal/api"
	"github.com/complynx/clx-scripter/internal/domain"
)

type controlsAuth struct{ readRevoked atomic.Bool }

func (a *controlsAuth) Authenticate(ctx context.Context, r *http.Request) (domain.Principal, error) {
	p, err := testAuth{}.Authenticate(ctx, r)
	if a.readRevoked.Load() {
		delete(p.Permissions, "chat.read")
	}
	return p, err
}
func requireControlReceipt(t *testing.T, raw []byte) {
	t.Helper()
	body := decode[map[string]any](t, raw)
	require.NotContains(t, body, "input")
	require.NotContains(t, body, "result")
	require.NotContains(t, body, "question")
	require.NotContains(t, body, "approval")
	require.NotContains(t, body, "settings_snapshot")
	require.NotContains(t, string(raw), "PRIVATE")
	require.Contains(t, body, "run_id")
	require.Contains(t, body, "status")
}
func TestControlOnlyReceiptsDoNotDiscloseChatAfterReadRevocation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, engine := fakeModel(t, f, func(int) any {
		return map[string]any{
			"id":     "private-question",
			"status": "completed",
			"output": []any{
				map[string]any{
					"type":      "function_call",
					"call_id":   "private-choice",
					"name":      "question",
					"arguments": `{"text":"PRIVATE choice question","choices":[{"id":"one","label":"PRIVATE option"}],"allow_text":false}`,
				},
			},
			"usage": map[string]int{"input_tokens": 10, "output_tokens": 5},
		}
	})
	run := f.createRun(t, runRequest("", "", "PRIVATE original request"), "private-run")
	require.NoError(t, engine.Execute(context.Background(), run.ID))
	require.Equal(t, domain.WaitingInput, f.run(t, run.ID).State)
	auth := &controlsAuth{}
	f.server.Close()
	f.server = httptest.NewServer(api.New(f.repository, auth, api.Options{}))
	t.Cleanup(f.server.Close)
	code, raw := f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/pause", "private-pause", nil)
	require.Equal(t, 200, code, string(raw))
	requireControlReceipt(t, raw)
	auth.readRevoked.Store(true)
	code, raw = f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/pause", "private-pause", nil)
	require.Equal(t, 200, code, string(raw))
	requireControlReceipt(t, raw)
	code, raw = f.request(
		t,
		"alice",
		"POST",
		"/v1/runs/"+run.ID+"/input",
		"private-steering",
		domain.RunInput{Kind: "steering", Text: "PRIVATE new steering"},
	)
	require.Equal(t, 200, code, string(raw))
	requireControlReceipt(t, raw)
	code, raw = f.request(
		t,
		"alice",
		"POST",
		"/v1/runs/"+run.ID+"/input",
		"private-steering",
		domain.RunInput{Kind: "steering", Text: "PRIVATE new steering"},
	)
	require.Equal(t, 200, code, string(raw))
	requireControlReceipt(t, raw)
	code, raw = f.request(t, "alice", "GET", "/v1/runs/"+run.ID, "", nil)
	require.Equal(t, 403, code, string(raw))
}
func TestDeletedChatRejectsOriginalIdempotencyKeysWithoutDisclosure(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	request := runRequest(f.session(t, "alice"), "", "PRIVATE deleted content")
	run := f.createRun(t, request, "deleted-original")
	code, raw := f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/pause", "delete-pause", nil)
	require.Equal(t, 200, code, string(raw))
	input := domain.RunInput{Kind: "steering", Text: "PRIVATE deleted steering"}
	code, raw = f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/input", "delete-input", input)
	require.Equal(t, 200, code, string(raw))
	code, raw = f.request(t, "alice", "DELETE", "/v1/chats/"+run.ChatID, "", nil)
	require.Equal(t, 204, code, string(raw))
	for _, call := range []struct {
		method, path, key string
		body              any
	}{{"POST", "/v1/runs", "deleted-original", request}, {"POST", "/v1/runs/" + run.ID + "/pause", "delete-pause", nil}, {"POST", "/v1/runs/" + run.ID + "/input", "delete-input", input}} {
		code, raw = f.request(t, "alice", call.method, call.path, call.key, call.body)
		require.Equal(t, 404, code, string(raw))
		require.NotContains(t, string(raw), "PRIVATE")
	}
}
