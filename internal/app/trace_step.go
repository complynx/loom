package app

import (
	"context"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/store"
	"github.com/complynx/loom/internal/telemetry"
)

type traceStepKey struct{}
type traceStepRevision struct {
	RunID    string
	Revision int64
}

func executionTraceStep(
	repository *store.Store,
	observer *telemetry.Telemetry,
) func(context.Context, domain.Run) (context.Context, func(error), error) {
	return func(ctx context.Context, run domain.Run) (context.Context, func(error), error) {
		snapshot, err := repository.ExecutionTrace(ctx, run.ID)
		if err != nil {
			return ctx, func(error) {}, err
		}
		revision := traceStepRevision{run.ID, snapshot.Revision}
		if previous, ok := ctx.Value(traceStepKey{}).(traceStepRevision); ok && previous == revision {
			return ctx, func(error) {}, nil
		}
		ctx, finish := observer.StartStep(
			ctx,
			run.ID,
			snapshot.Origin,
			snapshot.Parent,
			snapshot.Previous,
			snapshot.Inputs,
		)
		if err = repository.RecordTraceStep(
			ctx,
			run.ID,
			run.LeaseOwner,
			run.Fence,
			telemetry.CaptureTrace(ctx),
		); err != nil {
			finish(err)
			return ctx, func(error) {}, err
		}
		return context.WithValue(ctx, traceStepKey{}, revision), finish, nil
	}
}
