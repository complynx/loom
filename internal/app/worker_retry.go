package app

import (
	"context"
	"errors"
	"time"

	"github.com/riverqueue/river"

	"github.com/complynx/clx-scripter/internal/runs"
)

func (w *runWorker) executeWithRetry(ctx context.Context, job *river.Job[executeRun]) error {
	err := w.engine.Execute(ctx, job.Args.RunID)
	if deferred, ok := errors.AsType[*runs.RetryDeferredError](err); ok {
		if w.observer != nil {
			w.observer.Event(
				ctx,
				"provider retry scheduled",
				"run_id",
				job.Args.RunID,
				"next_attempt_at",
				deferred.Until,
			)
		}
		return river.JobSnooze(max(time.Millisecond, time.Until(deferred.Until)))
	}
	return err
}
