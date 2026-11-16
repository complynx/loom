package runs

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/provider"
)

func TestRecoveryContextTransientFailureRemainsRetryable(t *testing.T) {
	t.Parallel()
	failure := errors.New("temporary receipt storage failure")
	e := New(
		nil,
		nil,
		Options{RecoveryContext: func(context.Context, domain.Run) ([]provider.Item, error) { return nil, failure }},
	)
	_, err := e.recoveryContext(t.Context(), domain.Run{ID: "current", ReplacesRunID: "predecessor"})
	require.ErrorIs(t, err, failure)
}

func TestCompactionRestoresAuthoritativeRecoveryReceipt(t *testing.T) {
	t.Parallel()
	receipt := provider.CallOutput(
		"recovery_operation",
		json.RawMessage(`{"operation_id":"original","state":"succeeded"}`),
	)
	checkpoint := []provider.Item{
		provider.Message("user", "Book Tuesday"),
		receipt,
		provider.Message("user", "Use Wednesday instead"),
	}
	turn := &turnState{recoveryReceipts: []provider.Item{receipt}}
	raw, err := json.Marshal(provider.Response{Output: checkpoint})
	require.NoError(t, err)
	e := New(nil, nil, Options{})
	require.NoError(
		t,
		e.restoreActiveCompaction(
			t.Context(),
			domain.Run{Input: domain.Input{Text: "Book Tuesday"}},
			turn,
			domain.Step{Result: raw},
		),
	)
	require.Len(t, turn.items, 3)
	require.JSONEq(t, string(receipt), string(turn.items[1]))
	require.JSONEq(t, string(checkpoint[0]), string(turn.items[0]))
	require.JSONEq(t, string(checkpoint[2]), string(turn.items[2]))
	// A lossy checkpoint also receives the exact receipt, without replaying the
	// original request after the user's later correction.
	turn.items = restoreRecoveryReceipts([]provider.Item{checkpoint[0], checkpoint[2]}, []provider.Item{receipt})
	require.Len(t, turn.items, 3)
	require.JSONEq(t, string(receipt), string(turn.items[1]))
	require.JSONEq(t, string(checkpoint[2]), string(turn.items[2]))
}
