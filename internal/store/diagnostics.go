package store

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
)

const diagnosticsQueryTimeout = 2 * time.Second
const diagnosticsDetailLimit = 100

func diagnosticsAccess(p domain.Principal) error {
	if !p.Has("diagnostics.read") {
		return domain.Forbidden()
	}
	if p.OrgID == "" {
		return domain.NewError(http.StatusUnauthorized, "invalid_identity")
	}
	return nil
}

// DiagnosticsOverview reports only aggregates from the principal's organization.
func (s *Store) DiagnosticsOverview(ctx context.Context, p domain.Principal) (domain.DiagnosticsOverview, error) {
	out := domain.DiagnosticsOverview{GeneratedAt: time.Now().UTC(), Runs: map[string]int64{}}
	if err := diagnosticsAccess(p); err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, diagnosticsQueryTimeout)
	defer cancel()
	rows, err := s.pool.Query(ctx, "SELECT state,count(*) FROM runs WHERE org_id=$1 GROUP BY state", p.OrgID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var state string
		var count int64
		if err = rows.Scan(&state, &count); err != nil {
			rows.Close()
			return out, err
		}
		out.Runs[state] = count
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return out, err
	}
	err = s.pool.QueryRow(ctx, `SELECT
 (SELECT COALESCE(sum(reserved_microcredits),0) FROM runs WHERE org_id=$1),
 (SELECT count(*) FROM steps s JOIN runs r ON r.id=s.run_id WHERE r.org_id=$1 AND s.state='unknown' AND s.kind IN('model','summary','compact')),
 (SELECT count(*) FROM write_operations w JOIN runs r ON r.id=w.run_id WHERE r.org_id=$1 AND w.state IN('dispatching','pending','unknown')),
 (SELECT count(*) FROM platform_provider_attempts a JOIN runs r ON r.id=a.run_id WHERE r.org_id=$1 AND a.org_id=$1 AND a.status='unknown'),
 (SELECT COALESCE(sum(a.reserved_exposure),0) FROM platform_provider_attempts a JOIN runs r ON r.id=a.run_id WHERE r.org_id=$1 AND a.org_id=$1 AND a.status='unknown'),
 (SELECT COALESCE(max(greatest(0,EXTRACT(EPOCH FROM now()-state_entered_at))),0)::double precision FROM runs WHERE org_id=$1 AND state='queued'),
 (SELECT COALESCE(max(greatest(0,EXTRACT(EPOCH FROM now()-s.created_at))),0)::double precision FROM steps s JOIN runs r ON r.id=s.run_id WHERE r.org_id=$1 AND s.reserve>0 AND s.state IN('pending','unknown')),
 (SELECT count(*) FROM runs WHERE org_id=$1 AND state='running' AND lease_until<now())`, p.OrgID).Scan(
		&out.ReservedMicrocredits, &out.UnknownInferenceSteps, &out.UnresolvedWrites, &out.PlatformAttempts, &out.PlatformExposureMicrocredits, &out.OldestQueuedSeconds, &out.OldestReservationSeconds, &out.ExpiredLeases)
	if err != nil {
		return out, err
	}
	var queueExists bool
	if err = s.pool.QueryRow(ctx, "SELECT to_regclass('river_job') IS NOT NULL").Scan(&queueExists); err != nil {
		return out, err
	}
	if !queueExists {
		return out, nil
	}
	out.Queue = map[string]int64{}
	rows, err = s.pool.Query(
		ctx,
		`SELECT j.state::text,count(*) FROM river_job j JOIN runs r ON r.id::text=j.args->>'run_id' WHERE r.org_id=$1 AND j.kind='clx_execute_run' GROUP BY j.state`,
		p.OrgID,
	)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var count int64
		if err = rows.Scan(&state, &count); err != nil {
			return out, err
		}
		out.Queue[state] = count
	}
	return out, rows.Err()
}

const diagnosticRunColumns = `r.id::text,r.chat_id::text,r.state,r.fence,r.created_at,r.updated_at,r.lease_until,r.pause_requested,r.cancel_requested,r.spent_microcredits,r.reserved_microcredits,(SELECT count(*) FROM platform_provider_attempts a WHERE a.run_id=r.id AND a.org_id=r.org_id AND a.status='unknown')`

func scanDiagnosticRun(row pgx.Row) (domain.DiagnosticRun, error) {
	var r domain.DiagnosticRun
	err := row.Scan(
		&r.ID,
		&r.ChatID,
		&r.State,
		&r.Fence,
		&r.CreatedAt,
		&r.UpdatedAt,
		&r.LeaseUntil,
		&r.PauseRequested,
		&r.CancelRequested,
		&r.SpentMicrocredits,
		&r.ReservedMicrocredits,
		&r.PlatformAttempts,
	)
	return r, mapError(err)
}

// DiagnosticsRuns uses stable UUID keyset pagination, with nonterminal runs by default.
func (s *Store) DiagnosticsRuns(
	ctx context.Context,
	p domain.Principal,
	input domain.DiagnosticsRunsRequest,
) (domain.DiagnosticsRunsPage, error) {
	out := domain.DiagnosticsRunsPage{Items: []domain.DiagnosticRun{}}
	if err := diagnosticsAccess(p); err != nil {
		return out, err
	}
	switch domain.RunState(input.State) {
	case "",
		"all",
		domain.Queued,
		domain.Running,
		domain.WaitingInput,
		domain.WaitingApproval,
		domain.WaitingAuth,
		domain.Reconciling,
		domain.Paused,
		domain.Completed,
		domain.Cancelled,
		domain.Failed:
	default:
		return out, domain.NewError(http.StatusUnprocessableEntity, "invalid_state")
	}
	if input.AfterID != "" {
		cursor, err := uuid.Parse(input.AfterID)
		if err != nil {
			return out, domain.NewError(http.StatusUnprocessableEntity, "invalid_cursor")
		}
		input.AfterID = cursor.String()
	}
	if input.Limit == 0 {
		input.Limit = 50
	}
	if input.Limit < 1 || input.Limit > 100 {
		return out, domain.NewError(http.StatusUnprocessableEntity, "invalid_limit")
	}
	ctx, cancel := context.WithTimeout(ctx, diagnosticsQueryTimeout)
	defer cancel()
	rows, err := s.pool.Query(
		ctx,
		`SELECT `+diagnosticRunColumns+` FROM runs r WHERE r.org_id=$1 AND (($2='' AND r.state NOT IN('completed','cancelled','failed')) OR $2='all' OR r.state=$2) AND (NULLIF($3,'')::uuid IS NULL OR r.id>NULLIF($3,'')::uuid) ORDER BY r.id LIMIT $4`,
		p.OrgID,
		input.State,
		input.AfterID,
		input.Limit+1,
	)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		r, scanErr := scanDiagnosticRun(rows)
		if scanErr != nil {
			return out, scanErr
		}
		out.Items = append(out.Items, r)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if len(out.Items) > input.Limit {
		out.Items = out.Items[:input.Limit]
		out.Partial = true
		out.NextCursor = out.Items[len(out.Items)-1].ID
	}
	return out, nil
}

// DiagnosticsRun selects safe fields only; each related list returns the newest 100 rows.
func (s *Store) DiagnosticsRun(
	ctx context.Context,
	p domain.Principal,
	rid string,
) (domain.DiagnosticsRunDetail, error) {
	out := domain.DiagnosticsRunDetail{
		Steps:    []domain.DiagnosticStep{},
		Writes:   []domain.DiagnosticWrite{},
		Attempts: []domain.DiagnosticAttempt{},
	}
	if err := diagnosticsAccess(p); err != nil {
		return out, err
	}
	if _, err := uuid.Parse(rid); err != nil {
		return out, domain.NotFound()
	}
	ctx, cancel := context.WithTimeout(ctx, diagnosticsQueryTimeout)
	defer cancel()
	var err error
	out.Run, err = scanDiagnosticRun(
		s.pool.QueryRow(ctx, "SELECT "+diagnosticRunColumns+" FROM runs r WHERE r.id=$1 AND r.org_id=$2", rid, p.OrgID),
	)
	if err != nil {
		return out, err
	}
	retry, err := s.ProviderRetry(ctx, out.Run.ID)
	if err != nil {
		return out, err
	}
	if retry.Attempts > 0 {
		out.ProviderRetry = &retry
	}
	rows, err := s.pool.Query(ctx, `SELECT s.id::text,s.kind,s.state,s.reserve,s.created_at,
 COALESCE((s.usage->>'cache_write_tokens')::bigint,0),COALESCE((s.usage->>'cached_input_tokens')::bigint,0),COALESCE((s.usage->>'input_tokens')::bigint,0),COALESCE((s.usage->>'output_tokens')::bigint,0),COALESCE((s.usage->>'microcredits')::bigint,0)
 FROM steps s JOIN runs r ON r.id=s.run_id WHERE r.id=$1 AND r.org_id=$2 ORDER BY s.created_at DESC,s.id DESC LIMIT 101`, rid, p.OrgID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var v domain.DiagnosticStep
		if err = rows.Scan(
			&v.ID,
			&v.Kind,
			&v.State,
			&v.ReserveMicrocredits,
			&v.CreatedAt,
			&v.Usage.CacheWriteTokens,
			&v.Usage.CachedInputTokens,
			&v.Usage.InputTokens,
			&v.Usage.OutputTokens,
			&v.Usage.Microcredits,
		); err != nil {
			rows.Close()
			return out, err
		}
		out.Steps = append(out.Steps, v)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return out, err
	}
	if len(out.Steps) > diagnosticsDetailLimit {
		out.Steps = out.Steps[:100]
		out.StepsPartial = true
	}
	if err = s.diagnosticsWrites(ctx, p.OrgID, rid, &out); err != nil {
		return out, err
	}
	rows, err = s.pool.Query(
		ctx,
		`SELECT a.step_id::text,a.status,a.reserved_exposure,a.created_at FROM platform_provider_attempts a JOIN runs r ON r.id=a.run_id WHERE r.id=$1 AND r.org_id=$2 AND a.org_id=$2 ORDER BY a.created_at DESC,a.step_id DESC LIMIT 101`,
		rid,
		p.OrgID,
	)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var v domain.DiagnosticAttempt
		if err = rows.Scan(&v.StepID, &v.Status, &v.ReservedExposureMicrocredits, &v.CreatedAt); err != nil {
			return out, err
		}
		out.Attempts = append(out.Attempts, v)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if len(out.Attempts) > diagnosticsDetailLimit {
		out.Attempts = out.Attempts[:100]
		out.AttemptsPartial = true
	}
	return out, nil
}

func (s *Store) diagnosticsWrites(ctx context.Context, org, rid string, out *domain.DiagnosticsRunDetail) error {
	rows, err := s.pool.Query(
		ctx,
		`SELECT w.id::text,w.state,w.created_at,w.updated_at FROM write_operations w JOIN runs r ON r.id=w.run_id WHERE r.id=$1 AND r.org_id=$2 ORDER BY w.created_at DESC,w.id DESC LIMIT 101`,
		rid,
		org,
	)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v domain.DiagnosticWrite
		if err = rows.Scan(&v.ID, &v.State, &v.CreatedAt, &v.UpdatedAt); err != nil {
			rows.Close()
			return err
		}
		out.Writes = append(out.Writes, v)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	if len(out.Writes) > diagnosticsDetailLimit {
		out.Writes = out.Writes[:100]
		out.WritesPartial = true
	}
	return nil
}
