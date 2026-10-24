package runs

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/provider"
)

type reactionStore interface {
	React(context.Context, string, string, int64, string, domain.ReactionChange) (domain.ChatEvent, error)
}

func (e *Engine) executeReaction(
	ctx context.Context,
	run domain.Run,
	principal domain.Principal,
	call provider.Call,
) (json.RawMessage, bool, error) {
	var in domain.ReactionChange
	if err := decodeArguments(
		call.Arguments,
		&in,
	); err != nil || in.MessageSeq <= 0 ||
		!run.Interaction.Reactions.Allows(in.Reaction) {
		//nolint:nilerr // Argument errors are returned as model-visible data.
		return json.RawMessage(`{"error":"reaction_not_supported"}`), false, nil
	}
	if !principal.Has("chat.read") {
		return json.RawMessage(`{"error":"forbidden"}`), false, nil
	}
	repository, ok := e.store.(reactionStore)
	if !ok {
		return json.RawMessage(`{"error":"reaction_unavailable"}`), false, nil
	}
	event, err := repository.React(ctx, run.ID, e.opts.Owner, run.Fence, call.ID, in)
	if err != nil {
		var failure *domain.Error
		if errors.As(err, &failure) && failure.Status >= 400 && failure.Status < 500 && failure.Code != "lease_lost" {
			raw, marshalErr := json.Marshal(struct {
				Error string `json:"error"`
			}{failure.Code})
			return raw, false, marshalErr
		}
		return nil, false, err
	}
	raw, err := json.Marshal(event)
	return raw, false, err
}
