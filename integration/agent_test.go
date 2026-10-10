package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/complynx/clx-scripter/internal/domain"
	"github.com/complynx/clx-scripter/internal/identity"
	"github.com/complynx/clx-scripter/internal/provider"
	"github.com/complynx/clx-scripter/internal/runs"
	"github.com/stretchr/testify/require"
)

type modelFixture struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []json.RawMessage
	respond  func(int) any
}

func fakeModel(t *testing.T, f *fixture, respond func(int) any) (*modelFixture, *runs.Engine) {
	t.Helper()
	model := &modelFixture{respond: respond}
	model.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" || r.Header.Get("Authorization") != "Bearer test-provider-key" {
			w.WriteHeader(401)
			return
		}
		var raw json.RawMessage
		if json.NewDecoder(r.Body).Decode(&raw) != nil {
			w.WriteHeader(400)
			return
		}
		model.mu.Lock()
		model.requests = append(model.requests, raw)
		index := len(model.requests)
		model.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(model.respond(index))
	}))
	t.Cleanup(model.server.Close)
	client, err := provider.NewResponses(model.server.URL, "test-provider-key", model.server.Client())
	require.NoError(t, err)
	engine := runs.New(f.repository, client, runs.Options{Owner: "qa-worker", Authorize: func(ctx context.Context, run domain.Run) (domain.Principal, string, error) {
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://boundary", nil)
		req.Header.Set("Authorization", "Bearer "+run.ActorID)
		p, err := testAuth{}.Authenticate(ctx, req)
		return p, "user-token", err
	}})
	return model, engine
}
func finalResponse(text string) any {
	return map[string]any{"id": "provider-final", "status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": text}}}}, "usage": map[string]int{"input_tokens": 10, "output_tokens": 5}}
}
func (f *fixture) createRun(t *testing.T, request domain.CreateRun, key string) domain.Run {
	t.Helper()
	code, raw := f.request(t, "alice", "POST", "/v1/runs", key, request)
	require.Equal(t, 202, code, string(raw))
	return decode[domain.Run](t, raw)
}
func (f *fixture) run(t *testing.T, id string) domain.Run {
	t.Helper()
	code, raw := f.request(t, "alice", "GET", "/v1/runs/"+id, "", nil)
	require.Equal(t, 200, code, string(raw))
	return decode[domain.Run](t, raw)
}

func TestAgentFinalResultSurvivesNewWorkerAndAppearsInHistory(t *testing.T) {
	f := newFixture(t)
	model, engine := fakeModel(t, f, func(int) any { return finalResponse("Andrey is available at 15:00") })
	run := f.createRun(t, runRequest("", "", "Find a therapist"), "agent-final")
	require.NoError(t, engine.Execute(context.Background(), run.ID))
	result := f.run(t, run.ID)
	require.Equal(t, domain.Completed, result.State)
	require.Equal(t, "Andrey is available at 15:00", result.Result)
	require.EqualValues(t, 15, result.SpentMicrocredits)
	require.Zero(t, result.ReservedMicrocredits)
	code, raw := f.request(t, "alice", "GET", "/v1/chats/"+run.ChatID+"/history", "", nil)
	require.Equal(t, 200, code, string(raw))
	require.Contains(t, string(raw), result.Result)
	code, raw = f.request(t, "alice", "GET", "/v1/runs/"+run.ID+"/events", "", nil)
	require.Equal(t, 200, code, string(raw))
	require.Contains(t, string(raw), "completed")
	_, newWorker := fakeModel(t, f, func(int) any { t.Error("completed run must not infer again"); return finalResponse("unexpected") })
	_ = newWorker.Execute(context.Background(), run.ID)
	require.Equal(t, result.Result, f.run(t, run.ID).Result)
	model.mu.Lock()
	require.Len(t, model.requests, 1)
	model.mu.Unlock()
}
func TestQuestionChoiceResumeOnNewWorkerPreservesAnswer(t *testing.T) {
	f := newFixture(t)
	model, engine := fakeModel(t, f, func(index int) any {
		if index == 1 {
			return map[string]any{"id": "provider-question", "status": "completed", "output": []any{map[string]any{"type": "function_call", "call_id": "choose-therapist", "name": "question", "arguments": `{"text":"Which therapist?","choices":[{"id":"andrey","label":"Andrey"},{"id":"olga","label":"Olga"}],"allow_text":false}`}}, "usage": map[string]int{"input_tokens": 10, "output_tokens": 5}}
		}
		return finalResponse("Selected Andrey")
	})
	run := f.createRun(t, runRequest("", "", "Help choose"), "agent-question")
	require.NoError(t, engine.Execute(context.Background(), run.ID))
	waiting := f.run(t, run.ID)
	require.Equal(t, domain.WaitingInput, waiting.State)
	require.NotNil(t, waiting.Question)
	require.Len(t, waiting.Question.Choices, 2)
	code, raw := f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/input", "bad-choice", domain.RunInput{Kind: "answer", QuestionID: waiting.Question.ID, ChoiceIDs: []string{"unknown"}})
	require.Equal(t, 422, code, string(raw))
	code, raw = f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/input", "answer", domain.RunInput{Kind: "answer", QuestionID: waiting.Question.ID, ChoiceIDs: []string{"andrey"}})
	require.Equal(t, 200, code, string(raw))
	client, err := provider.NewResponses(model.server.URL, "test-provider-key", model.server.Client())
	require.NoError(t, err)
	newWorker := runs.New(f.repository, client, runs.Options{Owner: "qa-restarted-worker", Authorize: func(context.Context, domain.Run) (domain.Principal, string, error) {
		return domain.Principal{Subject: "alice", Permissions: map[string]bool{"runs.create": true, "chat.read": true}}, "token", nil
	}})
	require.NoError(t, newWorker.Execute(context.Background(), run.ID))
	require.Equal(t, domain.Completed, f.run(t, run.ID).State)
	model.mu.Lock()
	require.Len(t, model.requests, 2)
	require.Contains(t, string(model.requests[1]), "andrey")
	require.Contains(t, string(model.requests[1]), "function_call_output")
	model.mu.Unlock()
}
func TestSteeringAndPauseResumeReachInference(t *testing.T) {
	f := newFixture(t)
	model, engine := fakeModel(t, f, func(int) any { return finalResponse("Found a quiet evening appointment") })
	run := f.createRun(t, runRequest("", "", "Find appointment"), "steering-run")
	code, raw := f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/pause", "pause", nil)
	require.Equal(t, 200, code, string(raw))
	require.Equal(t, domain.Paused, f.run(t, run.ID).State)
	code, raw = f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/input", "steer", domain.RunInput{Kind: "steering", Text: "Only evening appointments please"})
	require.Equal(t, 200, code, string(raw))
	code, raw = f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/resume", "resume", nil)
	require.Equal(t, 200, code, string(raw))
	require.NoError(t, engine.Execute(context.Background(), run.ID))
	require.Equal(t, domain.Completed, f.run(t, run.ID).State)
	model.mu.Lock()
	require.Len(t, model.requests, 1)
	require.Contains(t, string(model.requests[0]), "Only evening appointments please")
	model.mu.Unlock()
}
func TestBudgetExhaustionDoesNotDispatchInference(t *testing.T) {
	f := newFixture(t)
	model, engine := fakeModel(t, f, func(int) any { t.Error("budget denied run reached provider"); return finalResponse("unexpected") })
	request := runRequest("", "", "This budget cannot cover even the reservation")
	request.Budget.MaxMicrocredits = 1
	run := f.createRun(t, request, "tiny-budget")
	require.NoError(t, engine.Execute(context.Background(), run.ID))
	result := f.run(t, run.ID)
	require.Equal(t, domain.Paused, result.State)
	require.Equal(t, "budget_exhausted", result.Reason)
	require.Zero(t, result.SpentMicrocredits)
	model.mu.Lock()
	require.Empty(t, model.requests)
	model.mu.Unlock()
}

func TestGracefulStopPersistsUnknownInferenceWithoutRepeatingIt(t *testing.T) {
	f := newFixture(t)
	started := make(chan struct{})
	providerStopped := make(chan struct{})
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&input)
		close(started)
		select {
		case <-r.Context().Done():
			close(providerStopped)
		case <-time.After(5 * time.Second):
			w.WriteHeader(http.StatusGatewayTimeout)
		}
	}))
	t.Cleanup(model.Close)
	client, err := provider.NewResponses(model.URL, "test-provider-key", model.Client())
	require.NoError(t, err)
	authorize := func(context.Context, domain.Run) (domain.Principal, string, error) {
		return domain.Principal{Subject: "alice", Permissions: map[string]bool{"runs.create": true, "chat.read": true}}, "token", nil
	}
	engine := runs.New(f.repository, client, runs.Options{Owner: "stop-worker", Authorize: authorize})
	run := f.createRun(t, runRequest("", "", "Long inference"), "shutdown-run")
	finished := make(chan error, 1)
	go func() { finished <- engine.Execute(context.Background(), run.ID) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("inference did not start")
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	before := time.Now()
	require.NoError(t, engine.Stop(stopCtx))
	require.Less(t, time.Since(before), time.Second)
	select {
	case err := <-finished:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
	result := f.run(t, run.ID)
	require.Equal(t, domain.Reconciling, result.State)
	require.NotEmpty(t, result.Reason)
	require.Positive(t, result.ReservedMicrocredits)
	restart := runs.New(f.repository, client, runs.Options{Owner: "new-worker", Authorize: authorize})
	_ = restart.Execute(context.Background(), run.ID)
	require.Equal(t, domain.Reconciling, f.run(t, run.ID).State)
	select {
	case <-providerStopped:
	case <-time.After(time.Second):
		t.Fatal("provider HTTP context was not cancelled")
	}
}

func TestRevokedExecutionAuthorizationWaitsWithoutInference(t *testing.T) {
	f := newFixture(t)
	model, _ := fakeModel(t, f, func(int) any { t.Error("revoked user reached model"); return finalResponse("unexpected") })
	client, err := provider.NewResponses(model.server.URL, "test-provider-key", model.server.Client())
	require.NoError(t, err)
	worker := runs.New(f.repository, client, runs.Options{Owner: "revoked-worker", Authorize: func(context.Context, domain.Run) (domain.Principal, string, error) {
		return domain.Principal{}, "", identity.ErrUnauthorized
	}})
	run := f.createRun(t, runRequest("", "", "Help"), "revoked-run")
	require.NoError(t, worker.Execute(context.Background(), run.ID))
	result := f.run(t, run.ID)
	require.Equal(t, domain.WaitingAuth, result.State)
	require.Zero(t, result.SpentMicrocredits)
	model.mu.Lock()
	require.Empty(t, model.requests)
	model.mu.Unlock()
}
