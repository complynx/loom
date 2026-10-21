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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/identity"
	"github.com/complynx/loom/internal/modules"
	"github.com/complynx/loom/internal/provider"
	"github.com/complynx/loom/internal/runs"
	"github.com/complynx/loom/internal/store"
	"github.com/complynx/loom/internal/telemetry"
)

type orphanProcessConfig struct {
	DSN      string `json:"dsn"`
	Provider string `json:"provider"`
	Issuer   string `json:"issuer"`
	Manifest string `json:"manifest"`
}

// Real PostgreSQL/River and SIGKILL; HTTP identity and provider peers are fixtures.
func TestOrphanQueueSurvivesSIGKILL(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("SIGKILL proof requires Linux")
	}
	f := newCallerRecoveryFixture(t)
	pool := f.repository.Pool()
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	require.NoError(t, err)
	_, err = migrator.Migrate(t.Context(), rivermigrate.DirectionUp, nil)
	require.NoError(t, err)
	// Fixture setup ends before the first worker starts. No repair SQL follows the kill.
	_, err = pool.Exec(
		t.Context(),
		"UPDATE runs SET state='queued',lease_owner='',lease_until=NULL WHERE id=$1",
		f.run.ID,
	)
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), `INSERT INTO river_job(args,kind,max_attempts,priority,queue)
 SELECT jsonb_build_object('run_id',id::text,'delivery_fence',fence),'clx_execute_run',5,1,'default' FROM runs WHERE id=$1`, f.run.ID)
	require.NoError(t, err)
	var calls atomic.Int32
	started := make(chan struct{})
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.URL.Path == "/responses/input_tokens" {
			_, _ = w.Write([]byte(`{"input_tokens":10}`))
			return
		}
		if calls.Add(1) == 1 {
			close(started)
			<-r.Context().Done()
			return
		}
		_, _ = w.Write(
			[]byte(
				`{"id":"recovered","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Recovered once"}]}],"usage":{"input_tokens":10,"output_tokens":2}}`,
			),
		)
	}))
	t.Cleanup(model.Close)
	path := filepath.Join(t.TempDir(), "process.json")
	raw, err := json.Marshal(
		orphanProcessConfig{
			DSN:      pool.Config().ConnString(),
			Provider: model.URL,
			Issuer:   f.issuer,
			Manifest: f.endpoint.URI,
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
	first := start()
	select {
	case <-started:
	case <-time.After(15 * time.Second):
		t.Fatal("worker did not enter provider inference")
	}
	require.NoError(t, first.Process.Kill())
	require.Error(t, first.Wait())
	start()
	require.Eventually(t, func() bool {
		run, loadErr := f.repository.ExecutionRun(t.Context(), f.run.ID)
		return loadErr == nil && run.State == domain.Completed
	}, 25*time.Second, 100*time.Millisecond)
	run, err := f.repository.ExecutionRun(t.Context(), f.run.ID)
	require.NoError(t, err)
	require.Equal(t, "Recovered once", run.Result)
	require.Equal(t, f.run.ID, run.ID)
	require.Equal(t, f.run.ChatID, run.ChatID)
	require.Equal(t, f.session.ID, run.AuthSessionID)
	require.EqualValues(t, 2, calls.Load())
	var exposureStatus string
	require.NoError(
		t,
		pool.QueryRow(t.Context(), "SELECT status FROM platform_provider_attempts WHERE run_id=$1", run.ID).
			Scan(&exposureStatus),
	)
	require.Equal(t, "unknown", exposureStatus)
	count, err := f.repository.SponsoredAttemptCount(t.Context(), run.ID)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

//nolint:paralleltest // Subprocess entry point; exits with the worker process.
func TestOrphanQueueProcess(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "orphan-worker" {
		return
	}
	raw, err := os.ReadFile(os.Args[len(os.Args)-1])
	require.NoError(t, err)
	var cfg orphanProcessConfig
	require.NoError(t, json.Unmarshal(raw, &cfg))
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, cfg.DSN)
	require.NoError(t, err)
	defer pool.Close()
	var queue *river.Client[pgx.Tx]
	repository := store.New(
		pool,
		func(ctx context.Context, tx pgx.Tx, id string) error { return insertRunJob(ctx, tx, queue, id) },
	)
	identityConfig := config.Identity{
		Issuer: cfg.Issuer, Audience: "harness", ClientID: "client", ClientSecret: "test-only",
		ServiceSubjects: []string{"actor"},
		RolePermissions: map[string][]string{"member": {"runs.create", "runs.control", "chat.read", "shuttle.read"}},
	}
	registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		return []modules.Endpoint{{ContainerID: "shuttle", URI: cfg.Manifest, Address: "127.0.0.1"}}, nil
	}), modules.Options{OnCallerBindings: rememberCallerBindings(identityConfig, repository)})
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(ctx))
	cipher, err := identity.NewSessionCipher("test", make([]byte, 32))
	require.NoError(t, err)
	check := func(ctx context.Context, p domain.Principal) (identity.Binding, error) {
		return callerBinding(ctx, registry, identityConfig, p)
	}
	auth, err := runtimeAuthenticator(
		config.Config{Identity: identityConfig},
		repository,
		registry,
		cipher,
		check,
		&sync.Map{},
	)
	require.NoError(t, err)
	logger := slog.New(slog.DiscardHandler)
	observer := telemetry.New(logger)
	model, err := provider.NewResponses(cfg.Provider, "test-only", http.DefaultClient)
	require.NoError(t, err)
	engine := runs.New(repository, model, runs.Options{
		Lease: 3 * time.Second, Heartbeat: time.Second, SponsoredAttemptLimit: 2,
		Authorize: executionAuthorizer(repository, auth, cipher, check, observer),
	})
	queue, _, err = runtimeQueue(
		config.Config{BootstrapSettings: config.Settings{RunActiveTimeSeconds: 600}},
		pool,
		engine,
		repository,
		observer,
		logger,
	)
	require.NoError(t, err)
	require.NoError(t, queue.Start(ctx))
	defer func() { _ = queue.Stop(context.Background()) }()
	recoverExhaustedQueue(ctx, repository, logger)
}
