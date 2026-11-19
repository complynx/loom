package runs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/provider"
)

const (
	stepLimitReason       = domain.StepLimitReason
	budgetExhaustedReason = domain.BudgetExhaustedReason
	limitNoteKind         = domain.LimitNoteStepKind
	limitNoteOutputTokens = 1024
	limitNoteInstruction  = "The work allowance for this request is used. Do not continue the work now and do not call tools. Write a short note in the language of the user: what is done, where the work stopped and what remains. State verified results only. Then ask whether to continue."
)

// pauseWithNote stops a run that used its step allowance or its own money
// allowance without a final result. Before the pause, the model writes one short
// progress note. The consumer shows the note and asks the user whether to
// continue. The note is one request that the run allowance does not limit; the
// wallet still limits it. The note is optional: a failure to write it never
// changes the pause. A run with no completed step has no progress to report.
func (e *Engine) pauseWithNote(
	ctx context.Context,
	run domain.Run,
	principal domain.Principal,
	token string,
	model domain.Model,
	turn *turnState,
	reason string,
) (bool, error) {
	if turn.limitNoteSaved {
		return false, e.finish(ctx, run, domain.Paused, reason, turn.limitNote)
	}
	if len(turn.steps) == 0 {
		return false, e.finish(ctx, run, domain.Paused, reason, "")
	}
	if reason == budgetExhaustedReason && !workSinceLimitNote(turn.steps) {
		// A larger request may no longer fit after the user continues. Do not
		// charge for another summary when only new input followed the last note.
		return false, e.finish(ctx, run, domain.Paused, reason, "")
	}
	request, ready := e.limitNoteRequest(ctx, run, principal, token, model, turn)
	raw, _ := json.Marshal(request)
	if !ready || len(raw) > e.opts.MaxInputBytes {
		return false, e.finish(ctx, run, domain.Paused, reason, "")
	}
	bound, boundErr := e.requestInputBound(ctx, run, model, request, len(raw))
	reserve, costErr := cost(bound, int64(request.MaxOutputTokens), model)
	if boundErr != nil || costErr != nil {
		return false, e.finish(ctx, run, domain.Paused, reason, "")
	}
	digest := sha256.Sum256(raw)
	intent, _ := json.Marshal(map[string]string{"request_hash": hex.EncodeToString(digest[:]), "reason": reason})
	step, err := e.store.BeginStep(ctx, run.ID, e.opts.Owner, run.Fence, limitNoteKind, intent, reserve)
	if err != nil {
		// No funds for the note, or a control request. The transition applies the latter.
		return false, e.finish(ctx, run, domain.Paused, reason, "")
	}
	request.ClientRequestID = step.ID
	inferCtx, cancel := context.WithTimeout(ctx, e.opts.InferenceTimeout)
	response, inferErr := e.callProvider(
		inferCtx,
		run,
		func(ctx context.Context) (provider.Response, error) { return e.model.Respond(ctx, request) },
	)
	cancel()
	// A definite provider failure only loses the note. It must not start durable
	// provider retries, which would replace the step-limit pause.
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
	return false, e.finish(ctx, run, domain.Paused, reason, note)
}

func workSinceLimitNote(steps []domain.Step) bool {
	for _, step := range slices.Backward(steps) {
		switch step.Kind {
		case limitNoteKind:
			return false
		case "input":
			continue
		default:
			return true
		}
	}
	return false
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
				provider.CallOutput(call.ID, json.RawMessage(`{"status":"not_executed","reason":"allowance_used"}`)),
			)
		}
	}
	return provider.Request{
		Provider:        model.Provider,
		Model:           model.ProviderModel,
		Effort:          noteEffort(model, run.Effort),
		Input:           append(items, provider.Message("developer", limitNoteInstruction)),
		Tools:           tools,
		MaxOutputTokens: min(model.MaxOutputTokens, limitNoteOutputTokens),
		Instructions:    conversationInstructions(run, ""),
	}, true
}

func noteEffort(model domain.Model, fallback string) string {
	if len(model.Efforts) > 0 {
		return model.Efforts[0]
	}
	return fallback
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
// called again. A failed note is saved too, with no text.
func replayLimitNote(turn *turnState, step domain.Step) {
	var response provider.Response
	turn.limitNoteSaved = true
	if json.Unmarshal(step.Result, &response) == nil && response.ErrorCode == "" {
		turn.limitNote = response.Text
		if response.Text != "" {
			turn.items = append(turn.items, provider.Message("assistant", response.Text))
		}
	}
}
