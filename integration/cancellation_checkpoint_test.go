package integration_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/clx-scripter/internal/domain"
	"github.com/complynx/clx-scripter/internal/runs"
	"github.com/complynx/clx-scripter/internal/store"
)

var errInterruptedReadUnavailable = errors.New("state read unavailable")

// interruptedReadStore holds the first owned state read until shutdown cancels it.
// The second read uses the real database to exercise the durable stop checkpoint.
type interruptedReadStore struct {
	*store.Store

	armed   atomic.Bool
	entered chan struct{}
	readErr error
}

func (s *interruptedReadStore) ClaimRun(
	ctx context.Context,
	id, owner string,
	lease time.Duration,
) (domain.Run, error) {
	run, err := s.Store.ClaimRun(ctx, id, owner, lease)
	if err == nil {
		s.armed.Store(true)
	}
	return run, err
}

func (s *interruptedReadStore) ExecutionRun(ctx context.Context, id string) (domain.Run, error) {
	if s.armed.CompareAndSwap(true, false) {
		close(s.entered)
		<-ctx.Done()
		if s.readErr != nil {
			return domain.Run{}, s.readErr
		}
		return domain.Run{}, ctx.Err()
	}
	return s.Store.ExecutionRun(ctx, id)
}

func TestInterruptedStateReadCheckpointsControl(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		command string
		state   domain.RunState
		reason  string
		readErr error
	}{
		{name: "cancel", command: "cancel", state: domain.Cancelled, reason: "client_cancel"},
		{name: "pause", command: "pause", state: domain.Paused, reason: "client_pause"},
		{name: "shutdown", state: domain.Paused, reason: "shutdown"},
		{
			name:    "cancel-with-database-error",
			command: "cancel",
			state:   domain.Cancelled,
			reason:  "client_cancel",
			readErr: errInterruptedReadUnavailable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			run := f.createRun(t, runRequest("", "", "Stop at durable read"), "interrupted-state-read")
			repository := &interruptedReadStore{Store: f.repository, entered: make(chan struct{}), readErr: tc.readErr}
			model := &recoveringProvider{}
			engine := runs.New(repository, model, recoveryOptions("interrupted-state-worker"))
			done := make(chan error, 1)
			go func() { done <- engine.Execute(context.Background(), run.ID) }()
			select {
			case <-repository.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("durable state read was not reached")
			}
			if tc.command != "" {
				code, raw := f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/"+tc.command, "read-control", nil)
				require.Equal(t, 200, code, string(raw))
				requireControlReceipt(t, raw)
			}
			stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			require.NoError(t, engine.Stop(stopCtx))
			resultErr := <-done
			if tc.readErr != nil {
				require.ErrorIs(t, resultErr, tc.readErr)
			} else {
				require.NoError(t, resultErr)
			}
			stopped := f.run(t, run.ID)
			require.Equal(t, tc.state, stopped.State)
			require.Equal(t, tc.reason, stopped.Reason)
			require.Empty(t, stopped.Result)
			require.Zero(t, stopped.ReservedMicrocredits)
			require.Zero(t, stopped.SpentMicrocredits)
			require.Empty(t, model.requests)
			if tc.state == domain.Cancelled {
				code, raw := f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/resume", "cancel-resume", nil)
				require.Equal(t, 409, code, string(raw))
			}
		})
	}
}
