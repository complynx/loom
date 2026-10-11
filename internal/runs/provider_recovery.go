package runs

import (
	"context"
	"errors"
	"time"

	"github.com/complynx/clx-scripter/internal/domain"
)

type sponsoredStore interface {
	AbsorbUnknownStep(context.Context, string, string, int64, string, int) error
	RecoverSponsoredRun(context.Context, string, int) (bool, error)
	SponsoredAttemptCount(context.Context, string) (int, error)
}

// Restart recovery needs fresh authorization before releasing funds or
// admitting another model request. Existing worker leases remain authoritative.
func (e *Engine) recoverSponsoredBeforeClaim(ctx context.Context, id string) error {
	store, enabled := e.sponsoredStore()
	if !enabled || e.opts.Authorize == nil {
		return nil
	}
	run, err := e.store.ExecutionRun(ctx, id)
	if err != nil {
		return err
	}
	principal, _, err := e.opts.Authorize(ctx, run)
	if !authorizedForRecovery(principal, err) {
		return nil
	}
	_, err = store.RecoverSponsoredRun(ctx, id, e.opts.SponsoredAttemptLimit)
	return err
}

func authorizedForRecovery(principal domain.Principal, err error) bool {
	return err == nil && canExecute(principal)
}

func (e *Engine) sponsoredStore() (sponsoredStore, bool) {
	store, ok := e.store.(sponsoredStore)
	return store, ok && e.opts.SponsoredAttemptLimit > 0
}

func (e *Engine) checkSponsoredLimit(ctx context.Context, run domain.Run) error {
	store, enabled := e.sponsoredStore()
	if !enabled {
		return nil
	}
	count, err := store.SponsoredAttemptCount(ctx, run.ID)
	if err != nil {
		return err
	}
	if count >= e.opts.SponsoredAttemptLimit {
		return e.stopTurn(ctx, run, domain.Paused, "platform_retry_limit")
	}
	return nil
}

// A lost response cannot dispatch tools. Its possible provider charge becomes
// platform exposure, while the successful response keeps normal billing.
func (e *Engine) recoverUnknownInference(ctx context.Context, run domain.Run, step domain.Step) (bool, error) {
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	fresh, err := e.store.ExecutionRun(saveCtx, run.ID)
	if err != nil {
		return false, err
	}
	store, enabled := e.sponsoredStore()
	if !enabled || fresh.PauseRequested || fresh.CancelRequested {
		return false, checkpointed(e.store.MarkStepUnknown(saveCtx, run.ID, e.opts.Owner, run.Fence, step.ID))
	}
	if err = store.AbsorbUnknownStep(
		saveCtx,
		run.ID,
		e.opts.Owner,
		run.Fence,
		step.ID,
		e.opts.SponsoredAttemptLimit,
	); err != nil {
		var conflict *domain.Error
		if errors.As(err, &conflict) && conflict.Code == "client_interruption" {
			return false, checkpointed(e.store.MarkStepUnknown(saveCtx, run.ID, e.opts.Owner, run.Fence, step.ID))
		}
		return false, err
	}
	if err = e.checkSponsoredLimit(saveCtx, run); err != nil {
		return false, err
	}
	return true, nil
}
