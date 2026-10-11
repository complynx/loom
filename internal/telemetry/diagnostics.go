package telemetry

import (
	"maps"
	"time"

	"github.com/complynx/clx-scripter/internal/domain"
)

// DiagnosticsHealth copies only bounded dependency observations under the metrics lock.
func (t *Telemetry) DiagnosticsHealth() domain.DiagnosticsHealth {
	t.mu.Lock()
	defer t.mu.Unlock()
	dependencies := maps.Clone(t.dependencies)
	return domain.DiagnosticsHealth{
		SampledAt:     t.sampled,
		UptimeSeconds: time.Since(t.started).Seconds(),
		Dependencies:  dependencies,
	}
}
