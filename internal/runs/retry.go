package runs

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/provider"
)

const providerRetryLimitReason = "provider_retry_limit"

type retryStore interface {
	ProviderRetry(context.Context, string) (domain.ProviderRetry, error)
	DeferProviderRetry(
		context.Context,
		string,
		string,
		int64,
		string,
		domain.ProviderRetry,
		int,
	) (domain.ProviderRetry, error)
}

// RetryDeferredError asks the durable worker to release its slot until Until.
type RetryDeferredError struct{ Until time.Time }

func (*RetryDeferredError) Error() string { return "provider retry scheduled" }

func (e *Engine) retryBeforeClaim(ctx context.Context, id string) error {
	store, ok := e.store.(retryStore)
	if !ok {
		return nil
	}
	retry, err := store.ProviderRetry(ctx, id)
	if err != nil {
		return err
	}
	run, err := e.store.ExecutionRun(ctx, id)
	if err != nil {
		return err
	}
	if run.State != domain.Queued || run.PauseRequested || run.CancelRequested {
		return nil
	}
	if retry.NextAttemptAt.After(time.Now()) && retry.NextAttemptAt.Before(retry.Deadline) {
		return &RetryDeferredError{Until: retry.NextAttemptAt}
	}
	return nil
}

func (e *Engine) callProvider(
	ctx context.Context,
	run domain.Run,
	call func(context.Context) (provider.Response, error),
) (provider.Response, error) {
	retries := e.opts.RetryShortLimit
	budget := e.opts.RetryShortBudget
	dispatch := call
	if store, ok := e.store.(retryStore); ok && e.opts.RetryAttemptLimit > 0 {
		state, err := store.ProviderRetry(ctx, run.ID)
		if err != nil {
			return provider.Response{}, &provider.Failure{Code: "retry_state_unavailable"}
		}
		retries = min(retries, max(0, e.opts.RetryAttemptLimit-state.Attempts-1))
		if !state.Deadline.IsZero() {
			budget = min(budget, max(0, time.Until(state.Deadline)))
			dispatch = func(ctx context.Context) (provider.Response, error) {
				if !state.Deadline.After(time.Now()) {
					return provider.Response{}, &provider.Failure{Code: providerRetryLimitReason}
				}
				return call(ctx)
			}
		}
	}
	return provider.ShortRetry(ctx, retries, budget, dispatch)
}

func (e *Engine) deferRejection(
	ctx context.Context,
	run domain.Run,
	step domain.Step,
	response provider.Response,
	inferErr error,
) (bool, error) {
	if failure, ok := errors.AsType[*provider.Failure](
		inferErr,
	); ok && failure.Code == providerRetryLimitReason &&
		!failure.UnknownUsage {
		return true, e.checkpointRetryLimit(ctx, run, step)
	}
	store, ok := e.store.(retryStore)
	if !ok || e.opts.RetryAttemptLimit <= 0 || !provider.Retryable(inferErr) || response.ID != "" ||
		len(response.Output) > 0 ||
		len(response.Calls) > 0 {
		return false, nil
	}
	failure, _ := errors.AsType[*provider.Failure](inferErr)
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	now := time.Now()
	retry := domain.ProviderRetry{
		Attempts: max(1, failure.RetryAttempts),
		Code:     failure.Code,
		Deadline: now.Add(e.opts.RetryWindow),
	}
	previous, err := store.ProviderRetry(saveCtx, run.ID)
	if err != nil {
		return true, err
	}
	retry.NextAttemptAt = now.Add(provider.RetryDelay(previous.Attempts+failure.RetryAttempts, failure.RetryAfter))
	retry, err = store.DeferProviderRetry(
		saveCtx,
		run.ID,
		e.opts.Owner,
		run.Fence,
		step.ID,
		retry,
		e.opts.RetryAttemptLimit,
	)
	if err != nil {
		return true, err
	}
	fresh, err := e.store.ExecutionRun(saveCtx, run.ID)
	if err != nil {
		return true, err
	}
	if fresh.State != domain.Queued {
		return true, errCheckpointedStop
	}
	return true, &RetryDeferredError{Until: retry.NextAttemptAt}
}

func (e *Engine) checkRetryBudget(ctx context.Context, run domain.Run) error {
	store, ok := e.store.(retryStore)
	if !ok {
		return nil
	}
	retry, err := store.ProviderRetry(ctx, run.ID)
	if err != nil {
		return err
	}
	if retry.Attempts > 0 && (retry.Attempts >= e.opts.RetryAttemptLimit || !retry.Deadline.After(time.Now())) {
		return e.stopTurn(ctx, run, domain.Paused, providerRetryLimitReason)
	}
	return nil
}

func (e *Engine) executeTurn(
	ctx context.Context,
	run domain.Run,
	principal domain.Principal,
	token string,
) (bool, error) {
	if err := e.checkRetryBudget(ctx, run); err != nil {
		return false, err
	}
	return e.executeAuthorizedTurn(ctx, run, principal, token)
}

func (e *Engine) checkpointRetryLimit(ctx context.Context, run domain.Run, step domain.Step) error {
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	raw, _ := json.Marshal(provider.Response{ErrorCode: providerRetryLimitReason})
	if err := e.store.CompleteStep(saveCtx, run.ID, e.opts.Owner, run.Fence, step.ID, raw, domain.Usage{}); err != nil {
		return err
	}
	return e.stopTurn(saveCtx, run, domain.Paused, providerRetryLimitReason)
}
