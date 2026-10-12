package runs

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/provider"
)

// PaidTool prepares admission and disclosure before the durable dispatch boundary.
type PaidTool interface {
	Prepare(context.Context, domain.Run, domain.Principal, string, provider.Call) (domain.PaidToolIntent, int64, error)
	Execute(
		context.Context,
		domain.Run,
		domain.Principal,
		string,
		domain.PaidToolIntent,
		string,
	) (json.RawMessage, domain.Usage, error)
}

const PaidToolRejected = "rejected"

// PaidToolError distinguishes definite rejection from an uncertain paid dispatch.
type PaidToolError struct {
	Disposition string
	Cause       error
}

func (e *PaidToolError) Error() string { return e.Cause.Error() }
func (e *PaidToolError) Unwrap() error { return e.Cause }

func (e *Engine) executePaidTool(
	ctx context.Context,
	run domain.Run,
	principal domain.Principal,
	token string,
	call provider.Call,
	tool PaidTool,
) (bool, error) {
	intent, reserve, err := tool.Prepare(ctx, run, principal, token, call)
	if err != nil {
		if suspension, ok := errors.AsType[*Suspension](err); ok {
			return true, e.stopTurn(ctx, run, suspension.State, suspension.Reason)
		}
		return e.completeRejectedPaidTool(ctx, run, call)
	}
	if intent.Call.ID != call.ID || intent.Call.Name != call.Name || reserve != intent.Billing.ReserveMicrocredits ||
		intent.Billing.Validate() != nil {
		return e.completeRejectedPaidTool(ctx, run, call)
	}
	payload, err := json.Marshal(intent)
	if err != nil {
		return true, err
	}
	step, err := e.store.BeginStep(ctx, run.ID, e.opts.Owner, run.Fence, "transcription", payload, reserve)
	if err != nil {
		return true, checkpointed(e.admissionFailure(ctx, run, err))
	}
	output, usage, callErr := tool.Execute(ctx, run, principal, token, intent, step.ID)
	if callErr != nil {
		classified, ok := errors.AsType[*PaidToolError](callErr)
		if !ok || (classified.Disposition != "not_dispatched" && classified.Disposition != PaidToolRejected) {
			return e.unknownPaidTool(ctx, run, step.ID)
		}
		output, usage = json.RawMessage(`{"error":"transcription_rejected"}`), domain.Usage{}
	} else {
		cost, costErr := domain.PaidToolCost(usage, intent.Billing)
		if costErr != nil || cost > reserve || !json.Valid(output) || len(output) > 524288 {
			return e.unknownPaidTool(ctx, run, step.ID)
		}
		usage.Microcredits = cost
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	err = e.store.CompleteStep(
		saveCtx,
		run.ID,
		e.opts.Owner,
		run.Fence,
		step.ID,
		provider.CallOutput(call.ID, output),
		usage,
	)
	return true, err
}

func (e *Engine) completeRejectedPaidTool(ctx context.Context, run domain.Run, call provider.Call) (bool, error) {
	payload, err := json.Marshal(call)
	if err != nil {
		return true, err
	}
	step, err := e.store.BeginStep(ctx, run.ID, e.opts.Owner, run.Fence, "tool", payload, 0)
	if err != nil {
		return true, checkpointed(e.admissionFailure(ctx, run, err))
	}
	output := provider.CallOutput(call.ID, json.RawMessage(`{"error":"transcription_rejected"}`))
	err = e.store.CompleteStep(ctx, run.ID, e.opts.Owner, run.Fence, step.ID, output, domain.Usage{})
	return true, err
}

func (e *Engine) unknownPaidTool(ctx context.Context, run domain.Run, sid string) (bool, error) {
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	err := e.store.MarkStepUnknown(saveCtx, run.ID, e.opts.Owner, run.Fence, sid)
	return true, checkpointed(err)
}
