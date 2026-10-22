package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/store"
)

func TestQueuedTraceGracefulProcessRestart(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("process SIGTERM acceptance requires Linux")
	}
	c := newQueueTraceReceiver(t)
	o := c.observer(t)
	var f *callerRecoveryFixture
	mux := http.NewServeMux()
	mux.HandleFunc("POST /accept", func(_ http.ResponseWriter, r *http.Request) {
		//nolint:contextcheck // Fixture resources use the test lifetime; only acceptance uses the request lifetime.
		f = newCallerRecoveryFixture(t, r.Context())
	})
	request := httptest.NewRequest(http.MethodPost, "/accept", nil)
	request.Header.Set("Traceparent", traceOrigin)
	o.HTTP(mux).ServeHTTP(httptest.NewRecorder(), request)
	pool := f.repository.Pool()
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	require.NoError(t, err)
	_, err = migrator.Migrate(t.Context(), rivermigrate.DirectionUp, nil)
	require.NoError(t, err)
	_, err = pool.Exec(
		t.Context(),
		"UPDATE runs SET state='queued',lease_owner='',lease_until=NULL WHERE id=$1",
		f.run.ID,
	)
	require.NoError(t, err)
	queue, _, err := runtimeQueue(config.Config{}, pool, nil, f.repository, o, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	repository := store.New(
		pool,
		func(ctx context.Context, tx pgx.Tx, id string) error { return insertRunJob(ctx, tx, queue, id) },
	)
	tx, err := pool.Begin(t.Context())
	require.NoError(t, err)
	require.NoError(t, insertRunJob(t.Context(), tx, queue, f.run.ID))
	require.NoError(t, tx.Commit(t.Context()))
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.URL.Path == "/responses/input_tokens" {
			_, _ = w.Write([]byte(`{"input_tokens":10}`))
			return
		}
		if calls.Add(1) == 1 {
			_, _ = w.Write(
				[]byte(
					`{"id":"question","status":"completed","output":[{"type":"function_call","call_id":"restart-question","name":"question","arguments":"{\"text\":\"Continue?\",\"choices\":[{\"id\":\"yes\",\"label\":\"Continue\"}],\"allow_text\":false}"}],"usage":{"input_tokens":10,"output_tokens":2}}`,
				),
			)
			return
		}
		_, _ = w.Write(
			[]byte(
				`{"id":"done","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Completed"}]}],"usage":{"input_tokens":10,"output_tokens":2}}`,
			),
		)
	}))
	t.Cleanup(model.Close)
	path := filepath.Join(t.TempDir(), "process.json")
	raw, err := json.Marshal(
		orphanProcessConfig{
			DSN:       pool.Config().ConnString(),
			Provider:  model.URL,
			Issuer:    f.issuer,
			Manifest:  f.endpoint.URI,
			Collector: c.server.URL,
		},
	)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	start := func() *exec.Cmd {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestOrphanQueueProcess$", "orphan-worker", path)
		require.NoError(t, cmd.Start())
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		return cmd
	}
	stop := func(cmd *exec.Cmd) {
		t.Helper()
		require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case waitErr := <-done:
			require.NoError(t, waitErr)
		case <-time.After(5 * time.Second):
			t.Fatal("graceful worker shutdown timed out")
		}
	}
	first := start()
	require.Eventually(t, func() bool {
		r, e := repository.ExecutionRun(t.Context(), f.run.ID)
		return e == nil && r.State == domain.WaitingInput
	}, 10*time.Second, 20*time.Millisecond)
	stop(first)
	_, inputTrace := tracedInput(
		t,
		o,
		repository,
		f.principal,
		f.run.ID,
		"answer",
		"00-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-bbbbbbbbbbbbbbbb-01",
		domain.RunInput{Kind: "answer", QuestionID: "restart-question", ChoiceIDs: []string{"yes"}},
	)
	second := start()
	require.Eventually(t, func() bool {
		r, e := repository.ExecutionRun(t.Context(), f.run.ID)
		return e == nil && r.State == domain.Completed
	}, 10*time.Second, 20*time.Millisecond)
	stop(second)
	require.EqualValues(t, 2, calls.Load())
	require.Eventually(t, func() bool { return len(c.executions()) == 2 }, 3*time.Second, 20*time.Millisecond)
	spans := c.executions()
	require.Equal(t, spans[0].GetTraceId(), spans[1].GetTraceId())
	require.Equal(t, spans[0].GetParentSpanId(), spans[1].GetParentSpanId())
	require.NotEqual(t, spans[0].GetSpanId(), spans[1].GetSpanId())
	require.Len(t, spans[1].GetLinks(), 1)
	require.Equal(t, spans[0].GetSpanId(), spans[1].GetLinks()[0].GetSpanId())
	_, _, applied, err := repository.RunTrace(t.Context(), f.run.ID)
	require.NoError(t, err)
	require.Equal(t, inputTrace, applied)
}
