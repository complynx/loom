package store_test

import (
	"context"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/store"
)

type recoveryQueryCounter struct {
	count     atomic.Int64
	recursive atomic.Int64
}

func (c *recoveryQueryCounter) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	c.count.Add(1)
	if strings.Contains(data.SQL, "WITH RECURSIVE") {
		c.recursive.Add(1)
	}
	return ctx
}
func (*recoveryQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestLongRecoveryChainUsesBoundedRoundTrips(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	p := principal("long-receipt-chain")
	r, err := s.CreateRun(t.Context(), p, "create", request())
	require.NoError(t, err)
	for i := range 40 {
		next, e := s.RecoverChatMessage(
			t.Context(),
			p,
			r.ChatID,
			strconv.Itoa(i),
			domain.RecoverMessage{Text: "Continue", ReplaceRunID: r.ID},
		)
		require.NoError(t, e)
		require.True(t, next.Delivered)
		r, err = s.GetRun(t.Context(), p, next.RunID)
		require.NoError(t, err)
	}
	counter := &recoveryQueryCounter{}
	config := s.Pool().Config()
	config.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, pool.Ping(t.Context()))
	repository := store.New(pool, nil)
	counter.count.Store(0)
	ops, err := repository.RecoveryOperations(t.Context(), r.ID)
	require.NoError(t, err)
	require.Empty(t, ops)
	require.LessOrEqual(t, counter.count.Load(), int64(4))
	require.Zero(t, counter.recursive.Load())
	// This still-open run may complete an operation. Its successor captures that
	// receipt once, rather than revisiting all forty empty ancestors each turn.
	source := r.ID
	_, err = s.Pool().
		Exec(t.Context(), `INSERT INTO write_operations(id,run_id,call_id,state,doc) VALUES(gen_random_uuid(),$1,'root-write','succeeded','{"Result":{"booking_id":"oldest"}}')`, source)
	require.NoError(t, err)
	next, err := s.RecoverChatMessage(
		t.Context(),
		p,
		r.ChatID,
		"after-operation",
		domain.RecoverMessage{Text: "Continue", ReplaceRunID: source},
	)
	require.NoError(t, err)
	require.True(t, next.Delivered)
	counter.count.Store(0)
	ops, err = repository.RecoveryOperations(t.Context(), next.RunID)
	require.NoError(t, err)
	require.Len(t, ops, 1)
	require.Equal(t, source, ops[0].RunID)
	require.LessOrEqual(t, counter.count.Load(), int64(4))
	require.Zero(t, counter.recursive.Load())
	counter.recursive.Store(0)
	_, err = s.Pool().Exec(t.Context(), "DELETE FROM write_operations WHERE run_id=$1", source)
	require.NoError(t, err)
	_, err = repository.RecoveryOperations(t.Context(), next.RunID)
	code(t, err, "recovery_context_unavailable")
	require.Zero(t, counter.recursive.Load())
}
