package runs

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/provider"
)

// Hold the checkpoint acknowledgement until the watcher observes the released
// lease and cancels execution, as it can after PostgreSQL commits the checkpoint.
type interactionCheckpointStore struct {
	Store

	mu       sync.Mutex
	run      domain.Run
	workDone <-chan struct{}
	deadline time.Time
}

func (s *interactionCheckpointStore) ExecutionRun(context.Context, string) (domain.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.run, nil
}

func (s *interactionCheckpointStore) release(ctx context.Context, state domain.RunState) error {
	s.mu.Lock()
	s.run.State = state
	s.run.LeaseOwner = ""
	s.deadline, _ = ctx.Deadline()
	s.mu.Unlock()
	<-s.workDone
	return ctx.Err()
}

func (s *interactionCheckpointStore) AskApproval(ctx context.Context, _, _ string, _ int64, _ domain.Approval) error {
	return s.release(ctx, domain.WaitingApproval)
}

func (s *interactionCheckpointStore) AskQuestion(ctx context.Context, _, _ string, _ int64, _ domain.Question) error {
	return s.release(ctx, domain.WaitingInput)
}

func TestInteractionCheckpointSurvivesLeaseReleaseCancellation(t *testing.T) {
	t.Parallel()
	for _, checkpoint := range []string{"approval", "steering-approval", "question", "conversation-reply"} {
		t.Run(checkpoint, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			run := domain.Run{ID: "run", State: domain.Running, Fence: 1, LeaseOwner: "worker"}
			run.Interaction.ReplyMode = domain.ReplyModeAgent
			approval := &domain.Approval{ID: "approval", OperationID: "operation"}
			if checkpoint == "conversation-reply" {
				run.Approval = approval
			}
			repository := &interactionCheckpointStore{run: run, workDone: ctx.Done()}
			moduleCall := func(context.Context, domain.Run, domain.Principal, string, provider.Call) (json.RawMessage, *domain.Approval, error) {
				return nil, approval, nil
			}
			engine := New(repository, nil, Options{Owner: "worker", ModuleCall: moduleCall, DiscardCall: moduleCall})
			done, watched := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(watched)
				engine.watch(ctx, cancel, run, done)
			}()
			defer func() { close(done); cancel(); <-watched }()
			var err error
			switch checkpoint {
			case "approval":
				_, _, err = engine.executeModuleCall(ctx, run, domain.Principal{}, "", provider.Call{ID: "call"})
			case "steering-approval":
				_, _, err = engine.dispatchPending(
					ctx,
					run,
					domain.Principal{},
					"",
					provider.Call{Name: toolModuleCall},
					domain.Settings{},
					true,
				)
			case "question":
				_, _, err = engine.executeQuestionCall(
					ctx,
					run,
					provider.Call{
						ID:        "question",
						Arguments: json.RawMessage(`{"text":"Continue?","choices":[],"allow_text":true}`),
					},
					domain.Settings{},
				)
			case "conversation-reply":
				_, err = engine.completeTurn(ctx, run, "Confirm the pending action?")
			}
			require.ErrorIs(t, ctx.Err(), context.Canceled, "the watcher must still cancel work after lease release")
			require.NoError(t, err, "execution cancellation must not fail the committed checkpoint")
			require.False(t, repository.deadline.IsZero(), "the checkpoint must remain time-bounded")
			require.WithinDuration(t, time.Now(), repository.deadline, time.Second)
		})
	}
}
