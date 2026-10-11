package domain

import "time"

// DiagnosticsHealth reports cached observations, not a synchronous dependency probe.
type DiagnosticsHealth struct {
	SampledAt     time.Time       `json:"sampled_at"`
	UptimeSeconds float64         `json:"uptime_seconds"`
	Dependencies  map[string]bool `json:"dependencies"`
}

type DiagnosticsSnapshot struct {
	Overview DiagnosticsOverview `json:"overview"`
	Runtime  DiagnosticsHealth   `json:"runtime"`
}
