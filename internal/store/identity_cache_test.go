package store_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIdentityCacheGenerationIsDurableAndAtomic(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ctx := context.Background()
	initial, err := s.IdentityCacheEpoch(ctx)
	require.NoError(t, err)
	require.Zero(t, initial)
	const count = 8
	results := make(chan int64, count)
	errors := make(chan error, count)
	var wg sync.WaitGroup
	for range count {
		wg.Go(func() { epoch, clearErr := s.ClearIdentityCache(ctx); results <- epoch; errors <- clearErr })
	}
	wg.Wait()
	close(results)
	close(errors)
	for clearErr := range errors {
		require.NoError(t, clearErr)
	}
	seen := map[int64]bool{}
	for epoch := range results {
		require.False(t, seen[epoch])
		seen[epoch] = true
	}
	final, err := s.IdentityCacheEpoch(ctx)
	require.NoError(t, err)
	require.EqualValues(t, count, final)
}
