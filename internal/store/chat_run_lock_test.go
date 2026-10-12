package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/store"
)

func TestFinalizationBlockedOnRunDoesNotDeadlockChatDeletion(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	p := principal("finalize-delete-locks")
	p.Permissions["chat.delete"] = true
	r := settledRunForDeletion(ctx, t, s, p)
	held, err := s.Pool().Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = held.Rollback(context.Background()) }()
	var blocker int
	require.NoError(t, held.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&blocker))
	_, err = held.Exec(ctx, "SELECT id FROM runs WHERE id=$1 FOR UPDATE", r.ID)
	require.NoError(t, err)
	finalized := make(chan error, 1)
	go func() { finalized <- s.Transition(ctx, r.ID, "worker", r.Fence, domain.Completed, "", "done") }()
	var finalizer int
	require.Eventually(t, func() bool {
		queryErr := s.Pool().
			QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) AND query LIKE 'SELECT %FROM runs WHERE id=%'`, blocker).
			Scan(&finalizer)
		return queryErr == nil
	}, time.Second, 5*time.Millisecond)
	deleted := make(chan error, 1)
	go func() { deleted <- s.DeleteChat(ctx, p, r.ChatID) }()
	require.Eventually(t, func() bool {
		var waiting bool
		queryErr := s.Pool().
			QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) AND query LIKE 'SELECT deleted_at%')`, finalizer).
			Scan(&waiting)
		return queryErr == nil && waiting
	}, time.Second, 5*time.Millisecond)
	require.NoError(t, held.Commit(ctx))
	require.NoError(t, <-finalized)
	require.NoError(t, <-deleted)
	_, err = s.GetRun(ctx, p, r.ID)
	code(t, err, "not_found")
	balance, err := s.Balance(ctx, p)
	require.NoError(t, err)
	require.EqualValues(t, 3, balance.SpentMicrocredits)
	require.Zero(t, balance.ReservedMicrocredits)
}

func settledRunForDeletion(ctx context.Context, t *testing.T, s *store.Store, p domain.Principal) domain.Run {
	t.Helper()
	chat, err := s.CreateChat(ctx, p)
	require.NoError(t, err)
	in := request()
	in.ChatID = chat.ID
	r, err := s.CreateRun(ctx, p, "finalize-delete", in)
	require.NoError(t, err)
	r, err = s.ClaimRun(ctx, r.ID, "worker", time.Minute)
	require.NoError(t, err)
	step, err := s.BeginStep(ctx, r.ID, "worker", r.Fence, "model", json.RawMessage(`{}`), 10)
	require.NoError(t, err)
	require.NoError(
		t,
		s.CompleteStep(
			ctx,
			r.ID,
			"worker",
			r.Fence,
			step.ID,
			json.RawMessage(`{"text":"done"}`),
			domain.Usage{Microcredits: 3},
		),
	)
	return r
}
