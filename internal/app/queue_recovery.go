package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/complynx/loom/internal/store"
)

const queueRecoveryInterval = 10 * time.Second

func insertRunJob(ctx context.Context, tx pgx.Tx, queue *river.Client[pgx.Tx], id string) error {
	var fence int64
	if err := tx.QueryRow(ctx, "SELECT fence FROM runs WHERE id=$1", id).Scan(&fence); err != nil {
		return err
	}
	_, err := queue.InsertTx(
		ctx,
		tx,
		executeRun{RunID: id, DeliveryFence: fence},
		&river.InsertOpts{MaxAttempts: queueMaxAttempts},
	)
	return err
}

// Exhausted deliveries pause before orphan recovery can issue another job.
// A replacement job carries the current fence; old River rows remain auditable.
func recoverExhaustedQueue(ctx context.Context, repository *store.Store, logger *slog.Logger) {
	timer := time.NewTicker(queueRecoveryInterval)
	defer timer.Stop()
	for {
		work, cancel := context.WithTimeout(ctx, maintenanceTimeout)
		result, err := repository.SweepQueueExhaustion(work, maintenanceBatch)
		logger.DebugContext(ctx, "queue recovery sweep finished", "component", "queue_recovery", "result", result)
		if err == nil {
			result, err = repository.SweepOrphanedRuns(work, maintenanceBatch)
			logger.DebugContext(ctx, "orphan recovery sweep finished", "component", "queue_recovery", "result", result)
		}
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
