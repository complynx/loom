package runs

import (
	"context"
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
