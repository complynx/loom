package app

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
)

func TestInitialKeyIsCompleteStableAndSharedAcrossConcurrentStarts(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "keys", "session-v1")
	var wait sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wait.Go(func() { results <- createSessionKey(path) })
	}
	wait.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	key, err := config.LoadEncryptionKey(path)
	require.NoError(t, err)
	require.Len(t, key, 32)
	require.NoError(t, createSessionKey(path))
	again, err := config.LoadEncryptionKey(path)
	require.NoError(t, err)
	require.Equal(t, key, again)
	require.NoError(t, os.WriteFile(path, []byte("broken existing key"), 0600))
	require.Error(t, createSessionKey(path))
	retained, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "broken existing key", string(retained))
}
