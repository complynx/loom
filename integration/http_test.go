package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/complynx/clx-scripter/internal/api"
	"github.com/complynx/clx-scripter/internal/domain"
	"github.com/complynx/clx-scripter/internal/identity"
	"github.com/complynx/clx-scripter/internal/store"
)

type testAuth struct{}

func (testAuth) Authenticate(_ context.Context, r *http.Request) (domain.Principal, error) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token != "alice" && token != "bob" {
		return domain.Principal{}, identity.ErrUnauthorized
	}
	return domain.Principal{
		Issuer:    "https://test.identity",
		Subject:   token,
		Actor:     token,
		OrgID:     "test-org",
		ExpiresAt: time.Now().Add(time.Hour),
		Permissions: map[string]bool{
			"chat.read":              true,
			"runs.create":            true,
			"runs.control":           true,
			"chat.events.append":     true,
			"preferences.self.write": true,
			"chat.delete":            true,
		},
	}, nil
}

type fixture struct {
	server     *httptest.Server
	repository *store.Store
	pool       *pgxpool.Pool
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv("CLX_TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("CLX_TEST_DATABASE_DSN is required for real PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	schema := "qa_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		if cleanupErr != nil {
			t.Errorf("remove isolated test schema: %v", cleanupErr)
		}
		admin.Close()
	})
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	q := parsed.Query()
	q.Set("search_path", schema)
	parsed.RawQuery = q.Encode()
	require.NoError(t, store.Migrate(ctx, parsed.String()))
	pool, err := pgxpool.New(ctx, parsed.String())
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	repository := store.New(pool, nil)
	require.NoError(
		t,
		repository.Bootstrap(
			ctx,
			domain.Settings{
				Models: []domain.Model{
					{
						ID:                           "test-model",
						ProviderModel:                "test-model",
						Efforts:                      []string{"low", "high"},
						MaxEffort:                    "high",
						MaxOutputTokens:              256,
						InputMicrocreditsPerMillion:  1000000,
						OutputMicrocreditsPerMillion: 1000000,
					},
				},
				DefaultModelID:         "test-model",
				DefaultEffort:          "low",
				InitialCredits:         100000,
				MaxRunMicrocredits:     10000,
				ActiveRunLimit:         8,
				HistoryRecentLimit:     50,
				MaxSteps:               10,
				QuestionTimeoutSeconds: 60,
			},
		),
	)
	server := httptest.NewServer(
		api.New(
			repository,
			testAuth{},
			api.Options{
				Ready:          pool.Ping,
				StreamPoll:     10 * time.Millisecond,
				StreamDuration: 200 * time.Millisecond,
				SealCredential: func(context.Context, *http.Request, domain.Principal) (json.RawMessage, error) {
					return json.RawMessage(`{"test_credential":"boundary-only"}`), nil
				},
			},
		),
	)
	t.Cleanup(server.Close)
	return &fixture{server, repository, pool}
}
func (f *fixture) request(t *testing.T, user, method, path, key string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		body = requestBody(body)
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, f.server.URL+path, reader)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+user)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	response, err := f.server.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, raw
}
func decode[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var value T
	require.NoError(t, json.Unmarshal(raw, &value), string(raw))
	return value
}
func (f *fixture) session(t *testing.T, user string) string {
	t.Helper()
	code, raw := f.request(t, user, "POST", "/v1/auth/sessions", "", struct{}{})
	require.Equal(t, 201, code, string(raw))
	return decode[map[string]any](t, raw)["auth_session_id"].(string)
}
func runRequest(session, chat, text string) domain.CreateRun {
	return domain.CreateRun{
		AuthSessionID: session,
		ChatID:        chat,
		Input:         domain.Input{Text: text},
		Interaction:   domain.Interaction{Questions: "choices"},
		Budget:        domain.Budget{MaxMicrocredits: 10000},
	}
}

func TestDefaultChatAndDurableRunContract(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	code, raw := f.request(t, "alice", "GET", "/v1/chats/default", "", nil)
	require.Equal(t, 200, code, string(raw))
	chat := decode[domain.Chat](t, raw)
	code, raw = f.request(t, "alice", "GET", "/v1/chats/default", "", nil)
	require.Equal(t, 200, code, string(raw))
	require.Equal(t, chat.ID, decode[domain.Chat](t, raw).ID)
	request := runRequest(f.session(t, "alice"), "", "Help choose a massage therapist")
	code, raw = f.request(t, "alice", "POST", "/v1/runs", "first-run", request)
	require.Equal(t, 202, code, string(raw))
	run := decode[domain.Run](t, raw)
	require.Equal(t, chat.ID, run.ChatID)
	require.Equal(t, domain.Queued, run.State)
	code, raw = f.request(t, "alice", "POST", "/v1/runs", "first-run", request)
	require.Equal(t, 202, code, string(raw))
	require.Equal(t, run.ID, decode[domain.Run](t, raw).ID)
	changed := request
	changed.Input.Text = "Different request"
	code, raw = f.request(t, "alice", "POST", "/v1/runs", "first-run", changed)
	require.Equal(t, 409, code, string(raw))
	require.Contains(t, string(raw), "idempotency_conflict")
	code, raw = f.request(t, "bob", "GET", "/v1/runs/"+run.ID, "", nil)
	require.Equal(t, 404, code, string(raw))
	code, raw = f.request(t, "alice", "GET", "/v1/chats/"+chat.ID+"/history", "", nil)
	require.Equal(t, 200, code, string(raw))
	history := decode[struct {
		Events []domain.ChatEvent `json:"events"`
	}](t, raw)
	require.Len(t, history.Events, 1)
	require.Equal(t, request.Input.Text, history.Events[0].Text)
	code, raw = f.request(t, "alice", "GET", "/v1/runs/"+run.ID+"/events", "", nil)
	require.Equal(t, 200, code, string(raw))
	require.Contains(t, string(raw), "event: run.status")
	require.Contains(t, string(raw), "id: 1")
	code, raw = f.request(t, "alice", "GET", "/v1/runs/"+run.ID+"/events?after=1", "", nil)
	require.Equal(t, 200, code, string(raw))
	require.NotContains(t, string(raw), "event: run.status")
}
func TestConcurrentSameChatAndIndependentChats(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	session := f.session(t, "alice")
	request := runRequest(session, "", "Start")
	var wait sync.WaitGroup
	codes := make(chan int, 2)
	for i := range 2 {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			raw, _ := json.Marshal(request)
			req, _ := http.NewRequest(http.MethodPost, f.server.URL+"/v1/runs", bytes.NewReader(raw))
			req.Header.Set("Authorization", "Bearer alice")
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", fmt.Sprintf("concurrent-%d", i))
			res, err := f.server.Client().Do(req)
			if err != nil {
				codes <- 0
				return
			}
			defer func() { _ = res.Body.Close() }()
			_, _ = io.Copy(io.Discard, res.Body)
			codes <- res.StatusCode
		}(i)
	}
	wait.Wait()
	close(codes)
	var statuses []int
	for code := range codes {
		statuses = append(statuses, code)
	}
	require.ElementsMatch(t, []int{202, 409}, statuses)
	code, raw := f.request(t, "alice", "POST", "/v1/chats", "", struct{}{})
	require.Equal(t, 201, code, string(raw))
	chat := decode[domain.Chat](t, raw)
	code, raw = f.request(t, "alice", "POST", "/v1/runs", "other-chat", runRequest(session, chat.ID, "Another chat"))
	require.Equal(t, 202, code, string(raw))
	code, raw = f.request(
		t,
		"bob",
		"POST",
		"/v1/runs",
		"wrong-owner",
		runRequest(f.session(t, "bob"), chat.ID, "Cannot access"),
	)
	require.Equal(t, 404, code, string(raw))
}
func TestImplicitSessionRetryIsIdempotent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	request := runRequest("", "", "Help")
	code, raw := f.request(t, "alice", "POST", "/v1/runs", "implicit-session", request)
	require.Equal(t, 202, code, string(raw))
	first := decode[domain.Run](t, raw)
	code, raw = f.request(t, "alice", "POST", "/v1/runs", "implicit-session", request)
	require.Equal(t, 202, code, string(raw))
	require.Equal(t, first.ID, decode[domain.Run](t, raw).ID)
}

func TestPassiveActionIsSearchableWithoutStartingRun(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	code, raw := f.request(t, "alice", "GET", "/v1/chats/default", "", nil)
	require.Equal(t, 200, code, string(raw))
	chat := decode[domain.Chat](t, raw)
	action := domain.AppendEvent{
		SourceEventID: "menu-1",
		Type:          "user.action",
		Action:        "menu.open",
		Text:          "Opened massage menu and selected Andrey",
		OccurredAt:    time.Now().UTC().Truncate(time.Second),
	}
	code, raw = f.request(t, "alice", "POST", "/v1/chats/"+chat.ID+"/events", "", action)
	require.Equal(t, 201, code, string(raw))
	first := decode[domain.ChatEvent](t, raw)
	require.Empty(t, first.RunID)
	code, raw = f.request(t, "alice", "POST", "/v1/chats/"+chat.ID+"/events", "", action)
	require.Equal(t, 201, code, string(raw))
	require.Equal(t, first.Seq, decode[domain.ChatEvent](t, raw).Seq)
	code, raw = f.request(
		t,
		"alice",
		"POST",
		"/v1/chats/"+chat.ID+"/history/search",
		"",
		domain.SearchRequest{Mode: "regex", Query: "(?i)andrey", Limit: 10},
	)
	require.Equal(t, 200, code, string(raw))
	result := decode[domain.SearchResult](t, raw)
	require.Len(t, result.Events, 1)
	require.Equal(t, action.Text, result.Events[0].Text)
	code, raw = f.request(t, "bob", "GET", "/v1/chats/"+chat.ID+"/history", "", nil)
	require.Equal(t, 404, code, string(raw))
	code, raw = f.request(t, "alice", "GET", "/v1/chats/"+chat.ID+"/history", "", nil)
	require.Equal(t, 200, code, string(raw))
	history := decode[struct {
		Events []domain.ChatEvent `json:"events"`
	}](t, raw)
	require.Len(t, history.Events, 1)
}
func TestPreferenceRevisionProtectsConcurrentUpdate(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	code, raw := f.request(t, "alice", "GET", "/v1/users/me/preferences", "", nil)
	require.Equal(t, 200, code, string(raw))
	initial := decode[domain.Preferences](t, raw)
	change := initial
	change.Effort = "high"
	code, raw = f.request(t, "alice", "PATCH", "/v1/users/me/preferences", "", change)
	require.Equal(t, 200, code, string(raw))
	updated := decode[domain.Preferences](t, raw)
	require.Equal(t, "high", updated.Effort)
	require.Greater(t, updated.Version, initial.Version)
	code, raw = f.request(t, "alice", "PATCH", "/v1/users/me/preferences", "", initial)
	require.Equal(t, 409, code, string(raw))
	code, raw = f.request(t, "alice", "GET", "/v1/users/me/preferences", "", nil)
	require.Equal(t, 200, code, string(raw))
	require.Equal(t, "high", decode[domain.Preferences](t, raw).Effort)
}

func TestDeleteDefaultChatTombstonesHistoryAndReplacesDefault(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	run := f.createRun(t, runRequest("", "", "Request in chat to delete"), "delete-chat-run")
	code, raw := f.request(t, "bob", "DELETE", "/v1/chats/"+run.ChatID, "", nil)
	require.Equal(t, 404, code, string(raw))
	code, raw = f.request(t, "alice", "DELETE", "/v1/chats/default", "", nil)
	require.Equal(t, 422, code, string(raw))
	for range 2 {
		code, raw = f.request(t, "alice", "DELETE", "/v1/chats/"+run.ChatID, "", nil)
		require.Equal(t, 204, code, string(raw))
	}
	code, raw = f.request(t, "alice", "GET", "/v1/chats/"+run.ChatID+"/history", "", nil)
	require.Equal(t, 404, code, string(raw))
	code, raw = f.request(t, "alice", "GET", "/v1/runs/"+run.ID, "", nil)
	require.Equal(t, 404, code, string(raw))
	code, raw = f.request(
		t,
		"alice",
		"POST",
		"/v1/runs",
		"deleted-chat-reuse",
		runRequest("", run.ChatID, "Cannot use deleted chat"),
	)
	require.Equal(t, 404, code, string(raw))
	code, raw = f.request(t, "alice", "GET", "/v1/chats/default", "", nil)
	require.Equal(t, 200, code, string(raw))
	replacement := decode[domain.Chat](t, raw)
	require.NotEqual(t, run.ChatID, replacement.ID)
	next := f.createRun(t, runRequest("", "", "New request after deletion"), "replacement-run")
	require.Equal(t, replacement.ID, next.ChatID)
}

func requestBody(body any) any {
	input, ok := body.(domain.RunInput)
	if !ok {
		return body
	}
	fields := map[string]any{"kind": input.Kind}
	if input.Text != "" {
		fields["text"] = input.Text
	}
	if input.QuestionID != "" {
		fields["question_id"] = input.QuestionID
	}
	if len(input.ChoiceIDs) > 0 {
		fields["choice_ids"] = input.ChoiceIDs
	}
	if input.ApprovalID != "" {
		fields["approval_id"] = input.ApprovalID
	}
	if input.Decision != "" {
		fields["decision"] = input.Decision
	}
	return fields
}
