package integration_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/clx-scripter/internal/api"
	"github.com/complynx/clx-scripter/internal/domain"
	"github.com/complynx/clx-scripter/internal/identity"
)

type revocableAuth struct{ revoked atomic.Bool }

func (a *revocableAuth) Authenticate(ctx context.Context, r *http.Request) (domain.Principal, error) {
	if a.revoked.Load() {
		return domain.Principal{}, identity.ErrUnauthorized
	}
	return testAuth{}.Authenticate(ctx, r)
}
func stream(t *testing.T, server *httptest.Server, id, cursor string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(
		http.MethodGet,
		server.URL+"/v1/runs/"+id+"/events",
		nil,
	)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer alice")
	if cursor != "" {
		req.Header.Set("Last-Event-ID", cursor)
	}
	res, err := server.Client().Do(req)
	require.NoError(t, err)
	require.Equal(t, 200, res.StatusCode)
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}
func TestSSELastEventIDAndRevocation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	run := f.createRun(t, runRequest("", "", "Help"), "sse-replay")
	code, raw := f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/pause", "sse-pause", nil)
	require.Equal(t, 200, code, string(raw))
	replay := stream(t, f.server, run.ID, "1")
	defer func() { _ = replay.Body.Close() }()
	body, err := io.ReadAll(replay.Body)
	require.NoError(t, err)
	require.NotContains(t, string(body), "id: 1\n")
	require.Contains(t, string(body), "id: 2\n")
	auth := &revocableAuth{}
	server := httptest.NewServer(
		api.New(
			f.repository,
			auth,
			api.Options{
				StreamPoll:          5 * time.Millisecond,
				StreamDuration:      5 * time.Second,
				ReauthorizeInterval: 10 * time.Millisecond,
			},
		),
	)
	t.Cleanup(server.Close)
	res := stream(t, server, run.ID, "")
	defer func() { _ = res.Body.Close() }()
	auth.revoked.Store(true)
	ended := make(chan error, 1)
	go func() { _, readErr := io.ReadAll(res.Body); ended <- readErr }()
	select {
	case resultErr := <-ended:
		require.NoError(t, resultErr)
	case <-time.After(time.Second):
		t.Fatal("revoked SSE did not close")
	}
}
func TestDisconnectPausesOnlyAfterLastStreamAndReconnectGrace(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	request := runRequest("", "", "Help")
	request.DisconnectPolicy = "pause"
	run := f.createRun(t, request, "disconnect-run")
	// Posting a one-shot request does not subscribe to SSE or trigger pause.
	require.Equal(t, domain.Queued, f.run(t, run.ID).State)
	server := httptest.NewServer(
		api.New(
			f.repository,
			testAuth{},
			api.Options{
				StreamPoll:      5 * time.Millisecond,
				StreamDuration:  5 * time.Second,
				DisconnectGrace: 80 * time.Millisecond,
			},
		),
	)
	t.Cleanup(server.Close)
	first := stream(t, server, run.ID, "")
	second := stream(t, server, run.ID, "")
	require.NoError(t, first.Body.Close())
	time.Sleep(120 * time.Millisecond)
	require.Equal(t, domain.Queued, f.run(t, run.ID).State)
	require.NoError(t, second.Body.Close())
	reconnected := stream(t, server, run.ID, "")
	time.Sleep(120 * time.Millisecond)
	require.Equal(t, domain.Queued, f.run(t, run.ID).State)
	require.NoError(t, reconnected.Body.Close())
	require.Eventually(
		t,
		func() bool { return f.run(t, run.ID).State == domain.Paused },
		time.Second,
		10*time.Millisecond,
	)
}
