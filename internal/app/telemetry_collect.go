package app

import (
	"context"
	"time"

	"github.com/complynx/clx-scripter/internal/store"
	"github.com/complynx/clx-scripter/internal/telemetry"
)

func collectTelemetry(
	ctx context.Context,
	repository *store.Store,
	observer *telemetry.Telemetry,
	trust func() error,
	sandboxReady func(context.Context) bool,
	root string,
) {
	timer := time.NewTicker(telemetryInterval)
	defer timer.Stop()
	for {
		work, cancel := context.WithTimeout(ctx, telemetryTimeout)
		snapshot, err := repository.TelemetrySnapshot(work)
		if ctx.Err() != nil {
			cancel()
			return
		}
		observer.Dependency(ctx, "database", err == nil)
		free, capacity, diskErr := artifactSpace(root)
		observer.Dependency(ctx, "artifact_volume", diskErr == nil)
		if err == nil {
			if diskErr == nil {
				snapshot.Gauges["clx_artifact_volume_free_bytes"] = free
				snapshot.Gauges["clx_artifact_volume_capacity_bytes"] = capacity
			}
			observer.Publish(snapshot)
		}
		cancel()
		observer.Dependency(ctx, "discovery", trust() == nil)
		probe, stop := context.WithTimeout(ctx, time.Second)
		available := sandboxReady(probe)
		if ctx.Err() != nil {
			stop()
			return
		}
		observer.Dependency(ctx, "sandbox", available)
		stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
