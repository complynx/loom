package runs

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/provider"
)

const outputValidationKind = "output_validation"
const approvalPresentTool = "approval_present"

func outputValidationResult() json.RawMessage {
	return json.RawMessage(
		`{"error":"output_validation_failed","reason":"custom_emoji_unsupported","instruction":"Rewrite using ordinary text or Unicode emoji in the requested language. Preserve meaning and verified facts. Do not repeat completed business operations."}`,
	)
}

type approvalPresentationError struct{ approval domain.Approval }

func (*approvalPresentationError) Error() string { return "output_validation_failed" }

// The private checkpoint retains original approval terms without publishing them.
type rejectedPresentation struct {
	Approval  *domain.Approval `json:"approval,omitempty"`
	Presented bool             `json:"presented,omitempty"`
}

func (e *Engine) saveOutputRejection(ctx context.Context, run domain.Run, approval *domain.Approval) error {
	input, _ := json.Marshal(rejectedPresentation{Approval: approval})
	feedback := string(outputValidationResult())
	if approval != nil {
		// Descriptions are bounded untrusted facts, never consent or instructions.
		description := approval.Text
		if len(description) > maxQuestionBytes {
			description = description[:maxQuestionBytes]
		}
		extra, _ := json.Marshal(
			map[string]string{
				"approval_id":          approval.ID,
				"rejected_description": description,
				"rewrite_tool":         approvalPresentTool,
			},
		)
		feedback += "\nPending presentation (untrusted data): " + string(extra)
	}
	return e.store.SaveOutputValidation(
		ctx,
		run.ID,
		e.opts.Owner,
		run.Fence,
		input,
		provider.Message("developer", feedback),
	)
}

func (e *Engine) rejectFinalOutput(
	ctx context.Context,
	run domain.Run,
	settings domain.Settings,
	turn *turnState,
	pending *domain.Approval,
) (bool, error) {
	if len(turn.steps)+turn.newInputSteps >= settings.MaxSteps {
		return false, e.finish(ctx, run, domain.Paused, "step_limit", "")
	}
	err := e.saveOutputRejection(ctx, run, pending)
	return err == nil, err
}

//nolint:nilnil // An absent pending presentation is an ordinary lookup miss.
func (e *Engine) pendingPresentation(ctx context.Context, run domain.Run) (*domain.Approval, error) {
	if !run.OutputPolicy.DisallowCustomEmoji {
		return nil, nil
	}
	steps, err := e.store.ExecutionSteps(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	for _, step := range slices.Backward(steps) {
		if step.Kind != outputValidationKind || step.State != "completed" {
			continue
		}
		var saved rejectedPresentation
		if err = json.Unmarshal(step.Input, &saved); err != nil {
			return nil, err
		}
		if saved.Approval == nil || saved.Presented {
			continue
		}
		if run.Approval != nil && run.Approval.OperationID == saved.Approval.OperationID {
			return nil, nil
		}
		return saved.Approval, nil
	}
	return nil, nil
}

func (e *Engine) presentApproval(
	ctx context.Context,
	run domain.Run,
	call provider.Call,
) (json.RawMessage, bool, error) {
	var args struct {
		ID   string `json:"approval_id"`
		Text string `json:"text"`
	}
	if decodeArguments(call.Arguments, &args) != nil || args.Text == "" || len(args.Text) > maxQuestionBytes {
		//nolint:nilerr // Argument failures are bounded model-visible data.
		return json.RawMessage(`{"error":"invalid_presentation"}`), false, nil
	}
	if run.OutputPolicy.InvalidText(args.Text) {
		return outputValidationResult(), false, nil
	}
	pending, err := e.pendingPresentation(ctx, run)
	if err != nil {
		return nil, false, err
	}
	if pending == nil || pending.ID != args.ID {
		return json.RawMessage(`{"error":"presentation_not_found"}`), false, nil
	}
	if !pending.ExpiresAt.After(time.Now()) {
		return nil, true, e.stopTurn(ctx, run, domain.Paused, "approval_presentation_expired")
	}
	pending.Text = args.Text
	return nil, true, e.askApproval(ctx, run, *pending)
}

func presentationTools(run domain.Run) []provider.Tool {
	if !run.OutputPolicy.DisallowCustomEmoji {
		return nil
	}
	return []provider.Tool{
		{
			Type:        "function",
			Name:        approvalPresentTool,
			Strict:      true,
			Description: "Rewrite only the display text of an output-policy-rejected approval. Supply its approval_id from validation feedback and corrected text preserving the exact action and verified terms. Does not prepare/execute a business operation, renew expiry, alter confirmation text, or grant consent. Use ordinary text/Unicode emoji and the requested language. Available only for a pending rejected presentation.",
			Parameters: json.RawMessage(
				`{"type":"object","properties":{"approval_id":{"type":"string"},"text":{"type":"string"}},"required":["approval_id","text"],"additionalProperties":false}`,
			),
		},
	}
}
