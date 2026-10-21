package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/store"
)

func TestOrphanRecoveryFencesAndDeliversOnce(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ctx := t.Context()
	migrator, err := rivermigrate.New(riverpgxv5.New(s.Pool()), nil)
	require.NoError(t, err)
	_, err = migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	require.NoError(t, err)
	repository := store.New(s.Pool(), func(ctx context.Context, tx pgx.Tx, id string) error {
		_, insertErr := tx.Exec(ctx, `INSERT INTO river_job(args,kind,max_attempts,priority,queue)
 SELECT jsonb_build_object('run_id',id::text,'delivery_fence',fence),'clx_execute_run',5,1,'default' FROM runs WHERE id=$1`, id)
		return insertErr
	})
	p := principal("alice")
	create := func(key string, expired bool) domain.Run {
		chat, createErr := s.CreateChat(ctx, p)
		require.NoError(t, createErr)
		in := request()
		in.ChatID = chat.ID
		run, createErr := s.CreateRun(ctx, p, key, in)
		require.NoError(t, createErr)
		run, createErr = s.ClaimRun(ctx, run.ID, "dead-worker", time.Minute)
		require.NoError(t, createErr)
		if expired {
			_, createErr = s.Pool().
				Exec(ctx, "UPDATE runs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", run.ID)
			require.NoError(t, createErr)
		}
		return run
	}
	orphan := create("orphan", true)
	live := create("live", false)
	paused := create("pause", true)
	cancelled := create("cancel", true)
	queued := create("delivery", true)
	_, err = s.Pool().Exec(ctx, "UPDATE runs SET pause_requested=true WHERE id=$1", paused.ID)
	require.NoError(t, err)
	_, err = s.Pool().Exec(ctx, "UPDATE runs SET cancel_requested=true WHERE id=$1", cancelled.ID)
	require.NoError(t, err)
	_, err = s.Pool().Exec(ctx, `INSERT INTO river_job(args,kind,max_attempts,priority,queue,state)
 VALUES($1,'clx_execute_run',5,1,'default','running'),($2,'clx_execute_run',5,1,'default','available')`,
		json.RawMessage(`{"run_id":"`+orphan.ID+`"}`), json.RawMessage(`{"run_id":"`+queued.ID+`"}`))
	require.NoError(t, err)
	var group sync.WaitGroup
	results := make(chan error, 2)
	counts := make(chan int, 2)
	for range 2 {
		group.Go(func() {
			count, sweepErr := repository.SweepOrphanedRuns(ctx, 100)
			counts <- count
			results <- sweepErr
		})
	}
	group.Wait()
	require.NoError(t, <-results)
	require.NoError(t, <-results)
	require.Equal(t, 1, <-counts+<-counts)
	got, err := s.GetRun(ctx, p, orphan.ID)
	require.NoError(t, err)
	require.Equal(t, domain.Queued, got.State)
	require.Equal(t, orphan.Fence+1, got.Fence)
	require.Equal(t, orphan.ChatID, got.ChatID)
	require.Empty(t, got.LeaseOwner)
	require.Error(t, s.Heartbeat(ctx, orphan.ID, "dead-worker", orphan.Fence, time.Minute))
	for _, run := range []domain.Run{live, paused, cancelled, queued} {
		got, err = s.GetRun(ctx, p, run.ID)
		require.NoError(t, err)
		require.Equal(t, domain.Running, got.State)
		require.Equal(t, run.Fence, got.Fence)
	}
	var jobs int
	require.NoError(
		t,
		s.Pool().
			QueryRow(ctx, `SELECT count(*) FROM river_job WHERE args->>'run_id'=$1 AND state='available'`, orphan.ID).
			Scan(&jobs),
	)
	require.Equal(t, 1, jobs)
	// A discarded replacement must not be hidden by the original stuck job.
	_, err = s.Pool().
		Exec(ctx, `UPDATE river_job SET state='discarded',finalized_at=now() WHERE args->>'run_id'=$1 AND state='available'`, orphan.ID)
	require.NoError(t, err)
	count, err := repository.SweepQueueExhaustion(ctx, 100)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	got, err = s.GetRun(ctx, p, orphan.ID)
	require.NoError(t, err)
	require.Equal(t, domain.Paused, got.State)
	// Resume creates a new generation. Its crash must not reuse historical exhaustion.
	_, err = repository.Control(ctx, p, orphan.ID, "resume-orphan", "resume")
	require.NoError(t, err)
	resumed, err := repository.ClaimRun(ctx, orphan.ID, "resumed-worker", time.Minute)
	require.NoError(t, err)
	_, err = s.Pool().
		Exec(ctx, `UPDATE river_job SET state='running' WHERE args->>'run_id'=$1 AND state='available'`, orphan.ID)
	require.NoError(t, err)
	_, err = s.Pool().
		Exec(ctx, "UPDATE runs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", orphan.ID)
	require.NoError(t, err)
	count, err = repository.SweepQueueExhaustion(ctx, 100)
	require.NoError(t, err)
	require.Zero(t, count)
	count, err = repository.SweepOrphanedRuns(ctx, 100)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	got, err = s.GetRun(ctx, p, orphan.ID)
	require.NoError(t, err)
	require.Equal(t, domain.Queued, got.State)
	require.Greater(t, got.Fence, resumed.Fence)
}

func TestOrphanRecoveryRollsBackFailedEnqueue(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ctx := t.Context()
	migrator, err := rivermigrate.New(riverpgxv5.New(s.Pool()), nil)
	require.NoError(t, err)
	_, err = migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	require.NoError(t, err)
	p := principal("alice")
	run, err := s.CreateRun(ctx, p, "orphan", request())
	require.NoError(t, err)
	run, err = s.ClaimRun(ctx, run.ID, "dead-worker", time.Millisecond)
	require.NoError(t, err)
	_, err = s.Pool().Exec(ctx, "UPDATE runs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", run.ID)
	require.NoError(t, err)
	rejected := errors.New("queue unavailable")
	repository := store.New(s.Pool(), func(context.Context, pgx.Tx, string) error { return rejected })
	_, err = repository.SweepOrphanedRuns(ctx, 100)
	require.ErrorIs(t, err, rejected)
	got, err := s.GetRun(ctx, p, run.ID)
	require.NoError(t, err)
	require.Equal(t, domain.Running, got.State)
	require.Equal(t, run.Fence, got.Fence)
}
