package runs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/complynx/clx-scripter/internal/domain"
	"github.com/complynx/clx-scripter/internal/provider"
	"time"
)

func standardTools(interaction domain.Interaction) []provider.Tool {
	tools := []provider.Tool{{Type: "function", Name: "script", Description: "Execute JavaScript against permitted service modules. Use modules.<module>.<method>(arguments); await calls; return JSON. No filesystem or direct network.", Parameters: json.RawMessage(`{"type":"object","properties":{"code":{"type":"string"}},"required":["code"],"additionalProperties":false}`), Strict: true}}
	// Even clients unable to answer need a durable waiting_input state, never guessed consent.
	tools = append(tools, provider.Tool{Type: "function", Name: "question", Description: "Ask a necessary question. Provide stable IDs for choices. This suspends the run until the user answers.", Parameters: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"},"choices":{"type":"array","items":{"type":"object","properties":{"id":{"type":"string"},"label":{"type":"string"}},"required":["id","label"],"additionalProperties":false}},"allow_text":{"type":"boolean"}},"required":["text","choices","allow_text"],"additionalProperties":false}`), Strict: true})
	return tools
}
func decodeArguments(raw json.RawMessage, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(out)
}
func (e *Engine) executeCall(ctx context.Context, run domain.Run, principal domain.Principal, token string, call provider.Call, settings domain.Settings) (json.RawMessage, bool, error) {
	switch call.Name {
	case "module_call":
		if e.opts.ModuleCall == nil {
			return json.RawMessage(`{"error":"module_call_unavailable"}`), false, nil
		}
		result, approval, err := e.opts.ModuleCall(ctx, run, principal, token, call)
		if err != nil {
			return nil, false, err
		}
		if approval != nil {
			return nil, true, e.store.AskApproval(ctx, run.ID, e.opts.Owner, run.Fence, *approval)
		}
		if !json.Valid(result) {
			return nil, false, errors.New("invalid module result")
		}
		return result, false, nil
	case "script":
		var args struct {
			Code string `json:"code"`
		}
		if err := decodeArguments(call.Arguments, &args); err != nil || args.Code == "" || len(args.Code) > 64<<10 {
			return json.RawMessage(`{"error":"invalid_script"}`), false, nil //nolint:nilerr // Invalid tool arguments are returned to the model as a structured result.
		}
		if e.opts.Script == nil {
			return json.RawMessage(`{"error":"script_unavailable"}`), false, nil
		}
		result, err := e.opts.Script(ctx, run, principal, token, args.Code)
		if err != nil {
			return nil, false, err
		}
		if !json.Valid(result) {
			return nil, false, errors.New("invalid script result")
		}
		return result, false, nil
	case "question":
		if run.Question != nil && run.Question.ID == call.ID && run.Question.Answer != nil {
			raw, _ := json.Marshal(run.Question.Answer)
			return raw, false, nil
		}
		var args struct {
			Text      string          `json:"text"`
			Choices   []domain.Choice `json:"choices"`
			AllowText bool            `json:"allow_text"`
		}
		if err := decodeArguments(call.Arguments, &args); err != nil || args.Text == "" || len(args.Text) > 8000 || len(args.Choices) > 20 {
			return json.RawMessage(`{"error":"invalid_question"}`), false, nil //nolint:nilerr // Invalid tool arguments are returned to the model as a structured result.
		}
		seen := map[string]bool{}
		for _, choice := range args.Choices {
			if choice.ID == "" || choice.Label == "" || seen[choice.ID] {
				return json.RawMessage(`{"error":"invalid_choices"}`), false, nil
			}
			seen[choice.ID] = true
		}
		switch run.Interaction.Questions {
		case "text":
			args.Choices = nil
			args.AllowText = true
		case "choices":
			args.AllowText = false
		case "none", "":
			args.AllowText = false
		}
		if len(args.Choices) == 0 && !args.AllowText && run.Interaction.Questions == "choices" {
			return json.RawMessage(`{"error":"choice_required"}`), false, nil
		}
		timeout := settings.QuestionTimeoutSeconds
		if timeout <= 0 {
			timeout = 86400
		}
		question := domain.Question{ID: call.ID, Text: args.Text, Choices: args.Choices, AllowText: args.AllowText, ExpiresAt: time.Now().Add(time.Duration(timeout) * time.Second)}
		return nil, true, e.store.AskQuestion(ctx, run.ID, e.opts.Owner, run.Fence, question)
	default:
		return json.RawMessage(`{"error":"unknown_tool"}`), false, nil
	}
}
