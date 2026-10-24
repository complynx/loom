package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
)

const (
	maxReactionsPerActor    = 32
	maxMessageReactionBytes = 16 << 10
)

// SetReaction records a passive user action. It never queues or resumes a run.
func (s *Store) SetReaction(
	ctx context.Context,
	p domain.Principal,
	cid string,
	in domain.SetReaction,
) (domain.ChatEvent, error) {
	var out domain.ChatEvent
	if !p.Has("chat.events.append") || !p.Has("chat.read") {
		return out, domain.Forbidden()
	}
	if in.SourceEventID == "" || len(in.SourceEventID) > 128 {
		return out, domain.NewError(http.StatusUnprocessableEntity, "invalid_event")
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		uid, err := s.ensureUser(ctx, tx, p)
		if err != nil {
			return err
		}
		if cid == defaultChatAlias {
			chat, chatErr := s.defaultChat(ctx, tx, uid, p.OrgID)
			if chatErr != nil {
				return chatErr
			}
			cid = chat.ID
		}
		// Lock the chat before its events, as all history writers do.
		var live bool
		if err = tx.QueryRow(ctx, "SELECT true FROM chats WHERE id=$1 AND owner_id=$2 AND org_id=$3 AND deleted_at IS NULL FOR UPDATE", cid, uid, p.OrgID).
			Scan(&live); err != nil {
			return mapError(err)
		}
		out, err = saveReaction(ctx, tx, cid, "", actor(p), "user:"+actor(p), in.SourceEventID, in.ReactionChange, "")
		return err
	})
	return out, err
}

// React checks the execution fence and negotiated capability on every dispatch.
// A saved receipt makes replay after a worker restart safe.
func (s *Store) React(
	ctx context.Context,
	rid, owner string,
	fence int64,
	callID string,
	in domain.ReactionChange,
) (domain.ChatEvent, error) {
	var out domain.ChatEvent
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := lockRunChat(ctx, tx, rid); err != nil {
			return err
		}
		run, err := leaseRun(ctx, tx, rid, owner, fence)
		if err != nil {
			return err
		}
		if run.State != domain.Running || callID == "" {
			return domain.Conflict("lease_lost")
		}
		if !run.Interaction.Reactions.Allows(in.Reaction) {
			return domain.NewError(http.StatusUnprocessableEntity, "reaction_not_supported")
		}
		sourceID := "reaction:" + hash([]string{rid, callID})
		description := ""
		for _, custom := range run.Interaction.Reactions.Custom {
			if in.Reaction == "custom:"+custom.ID {
				description = custom.Description
			}
		}
		out, err = saveReaction(ctx, tx, run.ChatID, rid, "agent", "assistant", sourceID, in, description)
		if err != nil {
			return err
		}
		return nil
	})
	return out, err
}

func reactionTarget(kind string) bool {
	switch kind {
	case "message.user", "message.assistant", "input.steering", "input.answer", "question.created", "approval.created":
		return true
	default:
		return false
	}
}

func saveReaction(
	ctx context.Context,
	tx pgx.Tx,
	cid, rid, source, reactor, sourceID string,
	in domain.ReactionChange,
	description string,
) (domain.ChatEvent, error) {
	var out domain.ChatEvent
	if in.MessageSeq <= 0 || domain.ValidateReactionValue(in.Reaction) != nil {
		return out, domain.NewError(http.StatusUnprocessableEntity, "invalid_reaction")
	}
	requestHash := hash(in)
	var previousChat, previousHash string
	var seq int64
	err := tx.QueryRow(ctx, "SELECT chat_id::text,seq,request_hash FROM passive_event_receipts WHERE source_actor=$1 AND source_event_id=$2", source, sourceID).
		Scan(&previousChat, &seq, &previousHash)
	if err == nil {
		if previousChat != cid || previousHash != requestHash {
			return out, domain.Conflict("event_conflict")
		}
		err = tx.QueryRow(ctx, "SELECT "+eventColumns+" FROM chat_events WHERE chat_id=$1 AND seq=$2", cid, seq).
			Scan(&out.Seq, &out.Type, &out.Action, &out.Text, &out.Data, &out.RunID, &out.SourceActor, &out.SourceEventID, &out.OccurredAt, &out.ReceivedAt, &out.Reactions, &out.ReactionsSeq)
		return out, mapError(err)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	var kind string
	var current map[string][]string
	if err = tx.QueryRow(ctx, "SELECT kind,reactions FROM chat_events WHERE chat_id=$1 AND seq=$2", cid, in.MessageSeq).
		Scan(&kind, &current); err != nil {
		return out, mapError(err)
	}
	if !reactionTarget(kind) {
		return out, domain.NewError(http.StatusUnprocessableEntity, "invalid_reaction_target")
	}
	raw, err := changedReactions(current, reactor, in)
	if err != nil {
		return out, err
	}
	data := struct {
		domain.ReactionChange

		Actor       string `json:"actor"`
		Description string `json:"description,omitempty"`
	}{in, reactor, description}
	out, err = chatEvent(ctx, tx, cid, rid, "message.reaction", "", source, data)
	if err != nil {
		return out, err
	}
	if _, err = tx.Exec(
		ctx,
		"UPDATE chat_events SET reactions=$3,reactions_seq=$4 WHERE chat_id=$1 AND seq=$2",
		cid,
		in.MessageSeq,
		raw,
		out.Seq,
	); err != nil {
		return out, err
	}
	_, err = tx.Exec(
		ctx,
		"UPDATE chat_events SET source_event_id=$3,request_hash=$4 WHERE chat_id=$1 AND seq=$2",
		cid,
		out.Seq,
		sourceID,
		requestHash,
	)
	if err != nil {
		return out, err
	}
	out.SourceEventID = sourceID
	// Attached state changed. Cached history prefixes must be built again.
	if _, err = tx.Exec(ctx, "DELETE FROM run_contexts WHERE chat_id=$1", cid); err != nil {
		return out, err
	}
	if rid != "" {
		err = runEvent(ctx, tx, rid, "message.reaction", out)
	}
	return out, err
}

func changedReactions(current map[string][]string, reactor string, in domain.ReactionChange) ([]byte, error) {
	values := current[reactor]
	index := slices.Index(values, in.Reaction)
	if in.Active && index < 0 {
		if len(values) >= maxReactionsPerActor {
			return nil, domain.Conflict("reaction_limit")
		}
		values = append(values, in.Reaction)
		slices.Sort(values)
	} else if !in.Active && index >= 0 {
		values = slices.Delete(values, index, index+1)
	}
	if len(values) == 0 {
		delete(current, reactor)
	} else {
		current[reactor] = values
	}
	raw, err := json.Marshal(current)
	if err == nil && len(raw) > maxMessageReactionBytes {
		return nil, domain.Conflict("reaction_limit")
	}
	return raw, err
}
