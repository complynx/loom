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
		work, cancel := context.WithTimeout(ctx, maintenanceTimeout)
		result, err := repository.SweepQueueExhaustion(work, maintenanceBatch)
		logger.DebugContext(ctx, "queue recovery sweep finished", "component", "queue_recovery", "result", result)
		cancel()
		if err != nil && ctx.Err() == nil {
			logger.ErrorContext(ctx,
				"queue recovery unavailable",
				"component",
				"queue_recovery",
				"error_code",
				"dependency_unavailable",
			)
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
