package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/complynx/clx-scripter/internal/domain"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/stretchr/testify/require"
)

func TestExhaustedQueueRecoveryPreservesUncertaintyAndLiveWork(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	migrator, e := rivermigrate.New(riverpgxv5.New(s.Pool()), nil)
	require.NoError(t, e)
	_, e = migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	require.NoError(t, e)
	create := func(key string) domain.Run {
		chat, e := s.CreateChat(ctx, p)
		require.NoError(t, e)
		in := request()
		in.ChatID = chat.ID
		r, e := s.CreateRun(ctx, p, key, in)
		require.NoError(t, e)
		return r
	}
	job := func(r domain.Run, state string) {
		_, e := s.Pool().Exec(ctx, `INSERT INTO river_job(args,kind,max_attempts,priority,queue,state,scheduled_at,finalized_at) VALUES($1,'clx_execute_run',5,1,'default',$2::river_job_state,now(),CASE WHEN $2::text IN('discarded','cancelled') THEN now() ELSE NULL END)`, json.RawMessage(`{"run_id":"`+r.ID+`"}`), state)
		require.NoError(t, e)
	}
	paused := create("paused")
	job(paused, "discarded")
	uncertain := create("uncertain")
	uncertain, e = s.ClaimRun(ctx, uncertain.ID, "worker", time.Minute)
	require.NoError(t, e)
	step, e := s.BeginStep(ctx, uncertain.ID, "worker", uncertain.Fence, "model", json.RawMessage(`{}`), 10)
	require.NoError(t, e)
	_, e = s.Pool().Exec(ctx, `UPDATE runs SET lease_until=now()-interval '1 second' WHERE id=$1`, uncertain.ID)
	require.NoError(t, e)
	job(uncertain, "discarded")
	writeUncertain := create("write")
	_, e = s.Pool().Exec(ctx, `INSERT INTO write_operations(id,run_id,call_id,state,doc) VALUES(gen_random_uuid(),$1,'call','dispatching','{}')`, writeUncertain.ID)
	require.NoError(t, e)
	job(writeUncertain, "discarded")
	live := create("live")
	job(live, "discarded")
	job(live, "available")
	leased := create("leased")
	leased, e = s.ClaimRun(ctx, leased.ID, "worker", time.Minute)
	require.NoError(t, e)
	job(leased, "discarded")
	count, e := s.SweepQueueExhaustion(ctx, 100)
	require.NoError(t, e)
	require.Equal(t, 3, count)
	got, e := s.GetRun(ctx, p, paused.ID)
	require.NoError(t, e)
	require.Equal(t, domain.Paused, got.State)
	require.Equal(t, "queue_attempts_exhausted", got.Reason)
	got, e = s.GetRun(ctx, p, uncertain.ID)
	require.NoError(t, e)
	require.Equal(t, domain.Reconciling, got.State)
	require.EqualValues(t, 10, got.ReservedMicrocredits)
	_, e = s.Control(ctx, p, uncertain.ID, "resume", "resume")
	code(t, e, "inference_uncertain")
	require.Error(t, s.CompleteStep(ctx, uncertain.ID, "worker", uncertain.Fence, step.ID, json.RawMessage(`{}`), domain.Usage{}))
	got, e = s.GetRun(ctx, p, writeUncertain.ID)
	require.NoError(t, e)
	require.Equal(t, domain.Reconciling, got.State)
	got, e = s.GetRun(ctx, p, live.ID)
	require.NoError(t, e)
	require.Equal(t, domain.Queued, got.State)
	got, e = s.GetRun(ctx, p, leased.ID)
	require.NoError(t, e)
	require.Equal(t, domain.Running, got.State)
	count, e = s.SweepQueueExhaustion(ctx, 100)
	require.NoError(t, e)
	require.Zero(t, count)
	_, e = s.Control(ctx, p, paused.ID, "resume", "resume")
	require.NoError(t, e)
	job(paused, "available")
	count, e = s.SweepQueueExhaustion(ctx, 100)
	require.NoError(t, e)
	require.Zero(t, count)
	paused, e = s.ClaimRun(ctx, paused.ID, "fresh", time.Minute)
	require.NoError(t, e)
	require.NoError(t, s.Transition(ctx, paused.ID, "fresh", paused.Fence, domain.Completed, "", "recovered"))
}
