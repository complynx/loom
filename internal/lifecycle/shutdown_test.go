package lifecycle_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/clx-scripter/internal/lifecycle"
)

func TestShutdownBoundsBlockedStopAndCancelsIt(t *testing.T) {
	t.Parallel()
	const budget = 250 * time.Millisecond
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	stopCancelled := make(chan struct{})
	cleanupBlocked := make(chan struct{})
	defer close(cleanupBlocked)
	started := time.Now()
	complete := lifecycle.Shutdown(parent, budget, []func(context.Context){func(ctx context.Context) {
		<-ctx.Done()
		close(stopCancelled)
	}}, func() { <-cleanupBlocked })
	require.False(t, complete)
	require.Less(t, time.Since(started), budget)
	select {
	case <-stopCancelled:
	case <-time.After(budget):
		t.Fatal("stop did not receive the cleanup deadline")
	}
}

func TestShutdownHealthyCleanupCompletesWithoutDelay(t *testing.T) {
	t.Parallel()
	stopped := make(chan struct{})
	cleaned := false
	started := time.Now()
	complete := lifecycle.Shutdown(
		context.Background(),
		5*time.Second,
		[]func(context.Context){func(ctx context.Context) {
			require.NoError(t, ctx.Err())
			close(stopped)
		}},
		func() {
			<-stopped
			cleaned = true
		},
	)
	require.True(t, complete)
	require.True(t, cleaned)
	require.Less(t, time.Since(started), time.Second)
}

func TestShutdownBoundsBlockedCleanup(t *testing.T) {
	t.Parallel()
	const budget = 250 * time.Millisecond
	cleanupStarted := make(chan struct{})
	cleanupBlocked := make(chan struct{})
	defer close(cleanupBlocked)
	started := time.Now()
	complete := lifecycle.Shutdown(context.Background(), budget, nil, func() {
		close(cleanupStarted)
		<-cleanupBlocked
	})
	require.False(t, complete)
	require.Less(t, time.Since(started), budget)
	select {
	case <-cleanupStarted:
	default:
		t.Fatal("cleanup was not reached")
	}
}

func TestShutdownCannotReportSuccessAfterDeadline(t *testing.T) {
	t.Parallel()
	require.False(t, lifecycle.Shutdown(context.Background(), 0, nil, func() {}))
}
