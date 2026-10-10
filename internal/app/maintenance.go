package app

import (
	"context"
	"github.com/complynx/clx-scripter/internal/artifacts"
	"log/slog"
	"time"
)

func reconcileArtifacts(ctx context.Context, files *artifacts.Service, logger *slog.Logger) {
	timer := time.NewTicker(time.Minute)
	defer timer.Stop()
	for {
		work, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, err := files.CollectDeleted(work, 100)
		if err == nil {
			_, err = files.Reconcile(work, time.Hour)
		}
		cancel()
		if err != nil && ctx.Err() == nil {
			logger.Warn("artifact reconciliation unavailable")
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
