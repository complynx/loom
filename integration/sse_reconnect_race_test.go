package integration_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/clx-scripter/internal/api"
	"github.com/complynx/clx-scripter/internal/domain"
)

func TestSSEReconnectCancelsBlockedDisconnectPause(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	request := runRequest("", "", "Help")
	request.DisconnectPolicy = "pause"
	run := f.createRun(t, request, "blocked-disconnect")
	server := httptest.NewServer(api.New(f.repository, testAuth{}, api.Options{
		StreamPoll:      5 * time.Millisecond,
		StreamDuration:  5 * time.Second,
		DisconnectGrace: 20 * time.Millisecond,
	}))
	t.Cleanup(server.Close)
	first := stream(t, server, run.ID, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lock, err := f.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = lock.Rollback(context.Background()) }()
	var blocker int
	require.NoError(t, lock.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&blocker))
	_, err = lock.Exec(ctx, "SELECT id FROM runs WHERE id=$1 FOR UPDATE", run.ID)
	require.NoError(t, err)
	require.NoError(t, first.Body.Close())
	// Observe the actual pause transaction waiting on PostgreSQL, after its
	// authorization and subscriber checks have completed.
	require.Eventually(t, func() bool {
		var waiting bool
		queryErr := f.pool.QueryRow(ctx, `SELECT EXISTS (
   SELECT 1 FROM pg_stat_activity
   WHERE $1 = ANY(pg_blocking_pids(pid)) AND query LIKE '%FROM runs%'
  )`, blocker).Scan(&waiting)
		return queryErr == nil && waiting
	}, time.Second, 5*time.Millisecond)
	reconnected := stream(t, server, run.ID, "")
	defer func() { _ = reconnected.Body.Close() }()
	require.NoError(t, lock.Rollback(ctx))
	time.Sleep(80 * time.Millisecond)
	require.Equal(t, domain.Queued, f.run(t, run.ID).State)
	require.NoError(t, reconnected.Body.Close())
	require.Eventually(t, func() bool {
		return f.run(t, run.ID).State == domain.Paused
	}, time.Second, 5*time.Millisecond)
}
