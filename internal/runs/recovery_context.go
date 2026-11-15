package runs

import (
	"context"
	"errors"
	"net/http"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/operations"
	"github.com/complynx/loom/internal/provider"
)

type recoveryOperationStore interface {
	RecoveryOperations(context.Context, string) ([]operations.Operation, error)
}

func (e *Engine) recoveryContext(ctx context.Context, run domain.Run) ([]provider.Item, error) {
	if run.ReplacesRunID == "" {
		return nil, nil
	}
	if e.opts.RecoveryContext != nil {
		items, err := e.opts.RecoveryContext(ctx, run)
		if err != nil {
			return nil, e.recoveryContextFailure(ctx, run, err)
		}
		return items, nil
	}
	// A custom engine without a receipt authorizer may continue a chain only if
	// the store proves that no operation receipts need disclosure.
	if repository, ok := e.store.(recoveryOperationStore); ok {
		ops, err := repository.RecoveryOperations(ctx, run.ID)
		if err != nil {
			return nil, e.recoveryContextFailure(ctx, run, err)
		}
		if len(ops) == 0 {
			return nil, nil
		}
	}
	return nil, e.stopTurn(ctx, run, domain.Paused, "recovery_context_unavailable")
}

func (e *Engine) recoveryContextFailure(ctx context.Context, run domain.Run, err error) error {
	if suspension, ok := errors.AsType[*Suspension](err); ok {
		return e.stopTurn(ctx, run, suspension.State, suspension.Reason)
	}
	denial, isDenial := errors.AsType[*domain.Error](err)
	if (isDenial && !denial.Retryable && denial.Status < http.StatusInternalServerError) ||
		errors.Is(err, operations.ErrBinding) {
		return e.stopTurn(ctx, run, domain.Paused, "recovery_context_unavailable")
	}
	return err
}
