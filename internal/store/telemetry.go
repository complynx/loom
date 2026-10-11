package store

import (
	"context"

	"github.com/complynx/loom/internal/telemetry"
)

// TelemetrySnapshot reads aggregates only. It never reads prompts, identities,
// credentials or blob contents. The caller bounds work with a collection deadline.
func (s *Store) TelemetrySnapshot(ctx context.Context) (telemetry.Snapshot, error) {
	out := telemetry.Snapshot{Gauges: map[string]float64{}, Runs: map[string]int64{}, Queue: map[string]int64{}}
	for _, query := range []struct {
		SQL    string
		Target map[string]int64
	}{
		{"SELECT state,count(*) FROM runs GROUP BY state", out.Runs},
		{"SELECT state::text,count(*) FROM river_job WHERE kind='clx_execute_run' GROUP BY state", out.Queue},
	} {
		rows, err := s.pool.Query(ctx, query.SQL)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var state string
			var count int64
			if scanErr := rows.Scan(&state, &count); scanErr != nil {
				rows.Close()
				return out, scanErr
			}
			query.Target[state] = count
		}
		rows.Close()
		if rowsErr := rows.Err(); rowsErr != nil {
			return out, rowsErr
		}
	}
	var reserved, unknownSteps, unknownWrites, reserveAge, queuedAge, artifactsBytes, tombstoneBytes float64
	var platformAttempts, platformExposure, expiredLeases, cancelRequested float64
	err := s.pool.QueryRow(ctx, `SELECT
 (SELECT COALESCE(sum(reserved),0)::double precision FROM billing_accounts),
 (SELECT count(*)::double precision FROM steps WHERE state='unknown' AND kind IN('model','summary','compact')),
 (SELECT count(*)::double precision FROM write_operations WHERE state IN('dispatching','pending','unknown')),
 (SELECT COALESCE(max(EXTRACT(EPOCH FROM now()-created_at)),0)::double precision FROM steps WHERE reserve>0 AND state IN('pending','unknown')),
 (SELECT COALESCE(max(EXTRACT(EPOCH FROM now()-state_entered_at)),0)::double precision FROM runs WHERE state='queued'),
 (SELECT COALESCE(sum(size),0)::double precision FROM artifacts WHERE collected_at IS NULL),
 (SELECT COALESCE(sum(size),0)::double precision FROM artifacts WHERE deleted_at IS NOT NULL AND collected_at IS NULL),
 (SELECT count(*)::double precision FROM platform_provider_attempts WHERE status='unknown'),
 (SELECT COALESCE(sum(reserved_exposure),0)::double precision FROM platform_provider_attempts WHERE status='unknown'),
 (SELECT count(*)::double precision FROM runs WHERE state='running' AND lease_until<now()),
 (SELECT count(*)::double precision FROM runs WHERE cancel_requested AND state NOT IN('completed','cancelled','failed'))
 `).Scan(&reserved, &unknownSteps, &unknownWrites, &reserveAge, &queuedAge, &artifactsBytes, &tombstoneBytes, &platformAttempts, &platformExposure, &expiredLeases, &cancelRequested)
	if err != nil {
		return out, err
	}
	out.Gauges = map[string]float64{
		"clx_reserved_microcredits":          reserved,
		"clx_unknown_inference_steps":        unknownSteps,
		"clx_unresolved_write_operations":    unknownWrites,
		"clx_oldest_reservation_age_seconds": max(0, reserveAge),
		"clx_oldest_queued_run_age_seconds":  max(0, queuedAge),
		"clx_artifacts_uncollected_bytes":    artifactsBytes,
		"clx_artifacts_tombstoned_bytes":     tombstoneBytes,
		"clx_unknown_platform_attempts":      platformAttempts,
		"clx_platform_exposure_microcredits": platformExposure,
		"clx_expired_run_leases":             expiredLeases,
		"clx_cancel_requested_runs":          cancelRequested,
	}
	return out, nil
}
