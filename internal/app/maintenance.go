package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/complynx/loom/internal/artifacts"
)

func reconcileArtifacts(ctx context.Context, files *artifacts.Service, logger *slog.Logger) {
	timer := time.NewTicker(time.Minute)
	defer timer.Stop()
	for {
		work, cancel := context.WithTimeout(ctx, maintenanceTimeout)
		_, err := files.CollectDeleted(work, maintenanceBatch)
		if err == nil {
			_, err = files.Reconcile(work, time.Hour)
		}
		cancel()
		if err == nil {
			logger.DebugContext(ctx, "artifact collection finished", "component", "artifact_gc")
		}
		if err != nil && ctx.Err() == nil {
			logger.ErrorContext(ctx,
				"artifact reconciliation unavailable",
				"component",
				"artifact_gc",
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
