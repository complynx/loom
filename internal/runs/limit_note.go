package runs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/provider"
)

const (
	stepLimitReason       = "step_limit"
	limitNoteKind         = "limit_note"
	limitNoteOutputTokens = 1024
	limitNoteInstruction  = "The step allowance for this request is used. Do not continue the work now and do not call tools. Write a short note in the language of the user: what is done, where the work stopped and what remains. State verified results only. Then ask whether to continue."
)

// pauseAtStepLimit stops a run that used its step allowance without a final
// result. Before the pause, the model writes one short progress note. The
// consumer shows the note and asks the user whether to continue. The note is
// optional: a failure to write it never changes the pause.
func (e *Engine) pauseAtStepLimit(
	ctx context.Context,
	run domain.Run,
	principal domain.Principal,
	token string,
	model domain.Model,
	turn *turnState,
) (bool, error) {
	if turn.limitNoteSaved {
		return false, e.finish(ctx, run, domain.Paused, stepLimitReason, turn.limitNote)
	}
	request, ready := e.limitNoteRequest(ctx, run, principal, token, model, turn)
	raw, _ := json.Marshal(request)
	reserve, costErr := cost(int64(len(raw))+requestTokenOverhead, int64(request.MaxOutputTokens), model)
	if !ready || costErr != nil || len(raw) > e.opts.MaxInputBytes {
		return false, e.finish(ctx, run, domain.Paused, stepLimitReason, "")
	}
	digest := sha256.Sum256(raw)
	intent, _ := json.Marshal(map[string]string{"request_hash": hex.EncodeToString(digest[:])})
	step, err := e.store.BeginStep(ctx, run.ID, e.opts.Owner, run.Fence, limitNoteKind, intent, reserve)
	if err != nil {
		// No funds for the note, or a control request. The transition applies the latter.
		return false, e.finish(ctx, run, domain.Paused, stepLimitReason, "")
	}
	request.ClientRequestID = step.ID
	inferCtx, cancel := context.WithTimeout(ctx, e.opts.InferenceTimeout)
	response, inferErr := e.callProvider(
		inferCtx,
		run,
		func(ctx context.Context) (provider.Response, error) { return e.model.Respond(ctx, request) },
	)
	cancel()
	if handled, retryErr := e.deferRejection(ctx, run, step, response, inferErr); handled {
		return false, retryErr
	}
	failure, known := errors.AsType[*provider.Failure](inferErr)
	if inferErr != nil && response.ID == "" && (!known || failure.UnknownUsage) {
		return e.recoverUnknownInference(ctx, run, step)
	}
	note := response.Text
	if inferErr != nil || response.Refused || len(response.Calls) > 0 || run.OutputPolicy.InvalidText(note) {
		response.ErrorCode, note = "limit_note_failed", ""
	}
	if err = e.completeLimitNote(ctx, run, model, step, response); err != nil {
		return false, err
	}
	return false, e.finish(ctx, run, domain.Paused, stepLimitReason, note)
}

// limitNoteRequest reuses the tools and items of the turn, so the provider can
// reuse its cached prefix. Calls that did not run get an explicit output.
func (e *Engine) limitNoteRequest(
	ctx context.Context,
	run domain.Run,
	principal domain.Principal,
	token string,
	model domain.Model,
	turn *turnState,
) (provider.Request, bool) {
	tools, err := e.listTurnTools(ctx, run, principal, token)
	if err != nil {
		return provider.Request{}, false
	}
	items := append([]provider.Item{}, turn.items...)
	for _, call := range turn.pending {
		if !turn.completed[call.ID] {
			items = append(
				items,
				provider.CallOutput(call.ID, json.RawMessage(`{"status":"not_executed","reason":"step_limit"}`)),
			)
		}
	}
	return provider.Request{
		Provider:        model.Provider,
		Model:           model.ProviderModel,
		Effort:          run.Effort,
		Input:           append(items, provider.Message("developer", limitNoteInstruction)),
		Tools:           tools,
		MaxOutputTokens: min(model.MaxOutputTokens, limitNoteOutputTokens),
		Instructions:    conversationInstructions(run),
	}, true
}

func (e *Engine) completeLimitNote(
	ctx context.Context,
	run domain.Run,
	model domain.Model,
	step domain.Step,
	response provider.Response,
) error {
	usage := domain.Usage{
		InputTokens:       response.Usage.InputTokens,
		OutputTokens:      response.Usage.OutputTokens,
		CachedInputTokens: response.Usage.CachedInputTokens,
		CacheWriteTokens:  response.Usage.CacheWriteTokens,
		RequestID:         response.ID,
	}
	charge, err := domain.UsageCost(usage, model)
	if err != nil {
		return err
	}
	usage.Microcredits = charge
	raw, _ := json.Marshal(response)
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	return e.store.CompleteStep(saveCtx, run.ID, e.opts.Owner, run.Fence, step.ID, raw, usage)
}

// replayLimitNote restores a saved note after a restart, so the model is not
// called again. A deferred provider retry leaves the note unsaved.
func replayLimitNote(turn *turnState, step domain.Step) {
	var response provider.Response
	if json.Unmarshal(step.Result, &response) != nil || response.ErrorCode == "provider_retry" {
		return
	}
	turn.limitNoteSaved = true
	if response.ErrorCode == "" {
		turn.limitNote = response.Text
	}
}
