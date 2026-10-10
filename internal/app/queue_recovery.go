package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/complynx/clx-scripter/internal/store"
)

// Queue exhaustion pauses work until an explicit resume. It never repeats an
// ambiguous inference or mutation and does not replenish automatic attempts.
func recoverExhaustedQueue(ctx context.Context, repository *store.Store, logger *slog.Logger) {
	timer := time.NewTicker(time.Minute)
	defer timer.Stop()
	for {
		work, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, err := repository.SweepQueueExhaustion(work, 100)
		cancel()
		if err != nil && ctx.Err() == nil {
			logger.Warn("queue recovery unavailable")
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
