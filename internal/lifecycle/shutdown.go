package lifecycle

import (
	"context"
	"sync"
	"time"
)

const (
	exitHeadroom       = time.Second
	exitBudgetFraction = 5
)

// Shutdown stops components in parallel and bounds cleanup. The total budget
// includes process/container exit, so cleanup leaves up to one second headroom.
// False means checkpoints or cleanup could not finish before the deadline.
func Shutdown(ctx context.Context, total time.Duration, stops []func(context.Context), cleanup func()) bool {
	margin := min(exitHeadroom, total/exitBudgetFraction)
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), total-margin)
	defer cancel()
	var group sync.WaitGroup
	for _, stop := range stops {
		group.Add(1)
		go func(stop func(context.Context)) {
			defer group.Done()
			stop(shutdownCtx)
		}(stop)
	}
	done := make(chan struct{})
	go func() {
		group.Wait()
		cleanup()
		close(done)
	}()
	select {
	case <-done:
		return shutdownCtx.Err() == nil
	case <-shutdownCtx.Done():
		return false
	}
}
