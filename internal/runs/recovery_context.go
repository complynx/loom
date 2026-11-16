package runs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/operations"
	"github.com/complynx/loom/internal/provider"
)

type recoveryOperationStore interface {
	RecoveryOperations(context.Context, string) ([]operations.Operation, error)
}

// Keep checkpoint steering in its original order. Replace any retained copies
// of these historical calls with freshly authorized receipts before the latest
// user item. Never append the originating request as a newer user instruction.
func restoreRecoveryReceipts(items, receipts []provider.Item) []provider.Item {
	ids := map[string]bool{}
	for _, raw := range receipts {
		var item struct {
			CallID string `json:"call_id"`
		}
		if json.Unmarshal(raw, &item) == nil && item.CallID != "" {
			ids[item.CallID] = true
		}
	}
	out := make([]provider.Item, 0, len(items)+len(receipts))
	lastUser := -1
	for _, raw := range items {
		var item struct {
			CallID string `json:"call_id"`
			Role   string `json:"role"`
		}
		_ = json.Unmarshal(raw, &item)
		if ids[item.CallID] || (len(receipts) > 0 && string(raw) == string(receipts[0])) {
			continue
		}
		if item.Role == "user" {
			lastUser = len(out)
		}
		out = append(out, raw)
	}
	if lastUser < 0 {
		lastUser = len(out)
	}
	return slices.Insert(out, lastUser, receipts...)
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
	if isDenial && denial.Code == "recovery_context_too_large" {
		// A size denial has another remedy than a lost authority or history.
		return e.stopTurn(ctx, run, domain.Paused, denial.Code)
	}
	if (isDenial && !denial.Retryable && denial.Status < http.StatusInternalServerError) ||
		errors.Is(err, operations.ErrBinding) {
		return e.stopTurn(ctx, run, domain.Paused, "recovery_context_unavailable")
	}
	return err
}
