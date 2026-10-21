package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/identity"
	"github.com/complynx/loom/internal/modules"
	"github.com/complynx/loom/internal/operations"
	"github.com/complynx/loom/internal/provider"
	"github.com/complynx/loom/internal/runs"
)

const (
	operationPrepared    = "prepared"
	operationPending     = "pending"
	operationDispatching = "dispatching"
	moduleFailureKey     = "error"
)

// Failure output contains only static classifications and an already bound ID.
// Raw backend messages, response bodies and credentials never enter tool output.
func (b *ModuleBridge) moduleCallFailure(
	ctx context.Context,
	run domain.Run,
	call provider.Call,
	failure error,
) (json.RawMessage, *domain.Approval, error) {
	if err := b.coordinator.InvalidateConsent(ctx, run.ID); err != nil {
		return nil, nil, err
	}
	op, found, err := b.coordinator.FindByCall(ctx, run.ID, call.ID)
	if err != nil {
		return nil, nil, err
	}
	if op.State == "unknown" || op.State == operationPending || op.State == operationDispatching {
		return nil, nil, &runs.Suspension{State: domain.Reconciling, Reason: operationUnknown}
	}
	code := "dependency_unavailable"
	switch {
	case errors.Is(failure, identity.ErrUnauthorized), errors.Is(failure, modules.ErrForbidden):
		code = "authority_changed"
	case errors.Is(failure, operations.ErrBinding):
		code = "operation_binding_changed"
	case errors.Is(failure, operations.ErrExpired):
		code = "operation_expired"
	case errors.Is(failure, operations.ErrApprovalRequired):
		code = "fresh_consent_required"
	case errors.Is(failure, modules.ErrUnavailable):
		code = "method_unavailable"
	}
	if denied, ok := errors.AsType[*domain.Error](failure); ok && (denied.Status == 401 || denied.Status == 403) {
		code = "authority_changed"
	}
	response := map[string]string{moduleFailureKey: code}
	if found {
		response["operation_id"] = op.ID
		response["state"] = op.State
	}
	raw, err := json.Marshal(response)
	if err != nil {
		return nil, nil, err
	}
	fresh, err := b.repository.ExecutionRun(ctx, run.ID)
	if err != nil {
		return nil, nil, err
	}
	if fresh.Approval != nil && fresh.Approval.Decision == "" && fresh.Approval.ExpiresAt.After(time.Now()) {
		fresh.Approval.FailureCode = code
		return raw, fresh.Approval, nil
	}
	if found && op.State == operationPrepared && op.Preparation.ExpiresAt.After(time.Now()) {
		text := op.Preparation.Description
		if text == "" {
			text = fmt.Sprintf("Confirm %s.%s", op.Module, op.Method)
		}
		return raw, &domain.Approval{
			ID: op.ID, OperationID: op.ID, Method: op.Module + "." + op.Method,
			Arguments: op.Preparation.Arguments, ArgumentHash: op.BindingHash, Text: text,
			ExpiresAt: op.Preparation.ExpiresAt, ReplyMode: fresh.Interaction.ReplyMode,
			FailureCode: code, Call: &domain.ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments},
		}, nil
	}
	return raw, nil, err
}

func terminalOperationFailure(op operations.Operation) json.RawMessage {
	code := "operation_failed"
	switch op.FailureCode {
	case "backend_rejected", "operation_expired", "execution_blocked":
		code = op.FailureCode
	}
	raw, _ := json.Marshal(map[string]string{"status": op.State, moduleFailureKey: code, "operation_id": op.ID})
	return raw
}
