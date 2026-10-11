package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/app"
	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/domain"
)

func TestDiagnosticsIsolationAndSafeDetail(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ctx := context.Background()
	p := principal("private-user")
	own, err := s.CreateRun(ctx, p, "own", request())
	require.NoError(t, err)
	_, err = s.DiagnosticsOverview(ctx, p)
	code(t, err, "forbidden")
	_, err = s.DiagnosticsRuns(ctx, p, domain.DiagnosticsRunsRequest{})
	code(t, err, "forbidden")
	_, err = s.DiagnosticsRun(ctx, p, own.ID)
	code(t, err, "forbidden")
	p.Permissions["diagnostics.read"] = true
	missingOrg := p
	missingOrg.OrgID = ""
	_, err = s.DiagnosticsRuns(ctx, missingOrg, domain.DiagnosticsRunsRequest{})
	code(t, err, "invalid_identity")
	other := principal("foreign-user")
	other.OrgID = "foreign-org"
	foreign, err := s.CreateRun(ctx, other, "foreign", request())
	require.NoError(t, err)
	_, err = s.DiagnosticsRun(ctx, p, foreign.ID)
	code(t, err, "not_found")
	_, err = s.DiagnosticsRun(ctx, p, uuid.NewString())
	code(t, err, "not_found")
	claimed, err := s.ClaimRun(ctx, own.ID, "worker", time.Minute)
	require.NoError(t, err)
	step, err := s.BeginStep(
		ctx,
		own.ID,
		"worker",
		claimed.Fence,
		"model",
		json.RawMessage(`{"prompt":"secret-input"}`),
		500,
	)
	require.NoError(t, err)
	require.NoError(t, s.MarkStepUnknown(ctx, own.ID, "worker", claimed.Fence, step.ID))
	_, err = s.Pool().
		Exec(ctx, `UPDATE runs SET state='running',reason='secret-error',cancel_requested=true,lease_until=now()-interval '1 minute' WHERE id=$1`, own.ID)
	require.NoError(t, err)
	_, err = s.Pool().
		Exec(ctx, `UPDATE steps SET usage='{"input_tokens":7,"request_id":"secret-provider"}',result='{"text":"secret-result"}' WHERE id=$1`, step.ID)
	require.NoError(t, err)
	_, err = s.Pool().
		Exec(ctx, `INSERT INTO write_operations(id,run_id,call_id,state,doc) VALUES($1,$2,'secret-call','unknown','{"arguments":"secret-write"}')`, uuid.NewString(), own.ID)
	require.NoError(t, err)
	_, err = s.Pool().
		Exec(ctx, `INSERT INTO platform_provider_attempts(step_id,run_id,owner_id,org_id,model_snapshot,client_request_id,reserved_exposure) VALUES($1,$2,$3,$4,'{"credentials":"secret-model"}','secret-request',777)`, step.ID, own.ID, own.OwnerID, p.OrgID)
	require.NoError(t, err)
	overview, err := s.DiagnosticsOverview(ctx, p)
	require.NoError(t, err)
	require.EqualValues(t, 500, overview.ReservedMicrocredits)
	require.EqualValues(t, 1, overview.UnknownInferenceSteps)
	require.EqualValues(t, 1, overview.UnresolvedWrites)
	require.EqualValues(t, 1, overview.PlatformAttempts)
	require.EqualValues(t, 777, overview.PlatformExposureMicrocredits)
	require.EqualValues(t, 1, overview.Runs["running"])
	require.EqualValues(t, 1, overview.ExpiredLeases)
	require.Nil(t, overview.Queue)
	detail, err := s.DiagnosticsRun(ctx, p, own.ID)
	require.NoError(t, err)
	require.Len(t, detail.Steps, 1)
	require.Len(t, detail.Writes, 1)
	require.Len(t, detail.Attempts, 1)
	require.EqualValues(t, 7, detail.Steps[0].Usage.InputTokens)
	require.Empty(t, detail.Steps[0].Usage.RequestID)
	require.EqualValues(t, 1, detail.Run.PlatformAttempts)
	encoded, err := json.Marshal(detail)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "secret")
	require.NotContains(t, string(encoded), "private-user")
	require.NoError(t, app.Migrate(ctx, config.Config{Database: config.Database{DSN: s.Pool().Config().ConnString()}}))
	_, err = s.Pool().
		Exec(ctx, `INSERT INTO river_job(kind,args) VALUES('clx_execute_run',jsonb_build_object('run_id',$1::text)),('clx_execute_run',jsonb_build_object('run_id',$2::text))`, own.ID, foreign.ID)
	require.NoError(t, err)
	overview, err = s.DiagnosticsOverview(ctx, p)
	require.NoError(t, err)
	require.EqualValues(t, 1, overview.Queue["available"])
	snapshot, err := s.TelemetrySnapshot(ctx)
	require.NoError(t, err)
	require.InDelta(t, 1, snapshot.Gauges["clx_unknown_platform_attempts"], 0)
	require.InDelta(t, 777, snapshot.Gauges["clx_platform_exposure_microcredits"], 0)
	require.InDelta(t, 1, snapshot.Gauges["clx_expired_run_leases"], 0)
	require.InDelta(t, 1, snapshot.Gauges["clx_cancel_requested_runs"], 0)
}

func TestDiagnosticsPaginationAndBounds(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ctx := context.Background()
	p := principal("reader")
	p.Permissions["diagnostics.read"] = true
	first, err := s.CreateRun(ctx, p, "first", request())
	require.NoError(t, err)
	_, err = s.Pool().Exec(ctx, "UPDATE runs SET state='completed' WHERE id=$1", first.ID)
	require.NoError(t, err)
	second, err := s.CreateRun(ctx, p, "second", request())
	require.NoError(t, err)
	page, err := s.DiagnosticsRuns(ctx, p, domain.DiagnosticsRunsRequest{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, second.ID, page.Items[0].ID)
	page, err = s.DiagnosticsRuns(ctx, p, domain.DiagnosticsRunsRequest{State: "all", Limit: 1})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.True(t, page.Partial)
	require.Equal(t, page.Items[0].ID, page.NextCursor)
	next, err := s.DiagnosticsRuns(
		ctx,
		p,
		domain.DiagnosticsRunsRequest{State: "all", Limit: 1, AfterID: page.NextCursor},
	)
	require.NoError(t, err)
	require.Len(t, next.Items, 1)
	require.False(t, next.Partial)
	require.Greater(t, next.Items[0].ID, page.Items[0].ID)
	for _, input := range []domain.DiagnosticsRunsRequest{{State: "oops"}, {AfterID: "oops"}, {Limit: 101}, {Limit: -1}} {
		_, err = s.DiagnosticsRuns(ctx, p, input)
		require.Error(t, err)
	}
	_, err = s.Pool().
		Exec(ctx, `INSERT INTO steps(id,run_id,ordinal,kind,state,input) SELECT gen_random_uuid(),$1,n,'tool','completed','{}' FROM generate_series(1,101) n`, second.ID)
	require.NoError(t, err)
	_, err = s.Pool().
		Exec(ctx, `INSERT INTO write_operations(id,run_id,call_id,state,doc) SELECT gen_random_uuid(),$1,n::text,'succeeded','{}' FROM generate_series(1,101) n`, second.ID)
	require.NoError(t, err)
	_, err = s.Pool().
		Exec(ctx, `INSERT INTO platform_provider_attempts(step_id,run_id,owner_id,org_id,model_snapshot,client_request_id,reserved_exposure) SELECT id,run_id,$2,$3,'{}','request',0 FROM steps WHERE run_id=$1`, second.ID, second.OwnerID, p.OrgID)
	require.NoError(t, err)
	detail, err := s.DiagnosticsRun(ctx, p, second.ID)
	require.NoError(t, err)
	require.Len(t, detail.Steps, 100)
	require.Len(t, detail.Writes, 100)
	require.Len(t, detail.Attempts, 100)
	require.True(t, detail.StepsPartial)
	require.True(t, detail.WritesPartial)
	require.True(t, detail.AttemptsPartial)
}

func TestDiagnosticsProviderRetryOrganizationScope(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ctx := context.Background()
	p := principal("retry-owner")
	p.Permissions["diagnostics.read"] = true
	run, err := s.CreateRun(ctx, p, "retry-diagnostics", request())
	require.NoError(t, err)
	detail, err := s.DiagnosticsRun(ctx, p, run.ID)
	require.NoError(t, err)
	require.Nil(t, detail.ProviderRetry)
	payload, err := json.Marshal(detail)
	require.NoError(t, err)
	require.NotContains(t, string(payload), "provider_retry")
	retry := domain.ProviderRetry{
		Attempts:      2,
		NextAttemptAt: time.Now().UTC().Truncate(time.Microsecond).Add(time.Minute),
		Deadline:      time.Now().UTC().Truncate(time.Microsecond).Add(time.Hour),
		Code:          "rate_limit_exceeded",
	}
	_, err = s.Pool().
		Exec(ctx, `INSERT INTO provider_retries(run_id,attempts,next_attempt_at,deadline,code) VALUES($1,$2,$3,$4,$5)`, run.ID, retry.Attempts, retry.NextAttemptAt, retry.Deadline, retry.Code)
	require.NoError(t, err)
	foreign := p
	foreign.OrgID = "foreign-org"
	hidden, err := s.DiagnosticsRun(ctx, foreign, run.ID)
	code(t, err, "not_found")
	require.Nil(t, hidden.ProviderRetry)
	reader := principal("another-org-operator")
	reader.Permissions["diagnostics.read"] = true
	detail, err = s.DiagnosticsRun(ctx, reader, run.ID)
	require.NoError(t, err)
	require.NotNil(t, detail.ProviderRetry)
	require.Equal(t, retry.Attempts, detail.ProviderRetry.Attempts)
	require.WithinDuration(t, retry.NextAttemptAt, detail.ProviderRetry.NextAttemptAt, time.Microsecond)
	require.WithinDuration(t, retry.Deadline, detail.ProviderRetry.Deadline, time.Microsecond)
	require.Equal(t, retry.Code, detail.ProviderRetry.Code)
}
