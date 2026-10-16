package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
)

// A message can commit while inference is producing a question. Do not strand it in a wait.
func (s *Store) conversationWait(
	ctx context.Context,
	tx pgx.Tx,
	r domain.Run,
	state domain.RunState,
) (domain.RunState, error) {
	if state == domain.Paused ||
		(r.Interaction.ReplyMode != domain.ReplyModeAgent && (r.Approval == nil || r.Approval.ReplyMode != domain.ReplyModeExactText)) {
		return state, nil
	}
	var pending bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM run_inputs WHERE run_id=$1 AND NOT applied AND doc->>'kind'='steering')`, r.ID).
		Scan(&pending); err != nil {
		return state, err
	}
	if !pending {
		return state, nil
	}
	if s.enqueue != nil {
		if err := s.enqueue(ctx, tx, r.ID); err != nil {
			return state, err
		}
	}
	return domain.Queued, nil
}

// ResolveInteraction records the model's interpretation of an accepted user message.
// It never dispatches an operation. Dispatch still checks fresh authority and consent revision.
func (s *Store) ResolveInteraction(ctx context.Context, rid, owner string, fence int64,
	callID string, decision domain.InteractionResolution,
) (domain.Run, error) {
	var r domain.Run
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := lockRunChat(ctx, tx, rid); err != nil {
			return err
		}
		var err error
		r, err = leaseRun(ctx, tx, rid, owner, fence)
		if err != nil {
			return err
		}
		if err = validateResolution(r, callID, decision); err != nil {
			return err
		}
		input, err := acceptedResolutionInput(ctx, tx, r, decision)
		if err != nil {
			return err
		}
		a := r.Approval
		if a.Decision != "" {
			return replayResolution(a, callID, decision)
		}
		if decision.Decision == domain.DecisionApprove && !a.ExpiresAt.After(time.Now()) {
			return domain.Conflict("approval_expired")
		}
		if s.approval == nil {
			return domain.Conflict("approval_handler_unavailable")
		}
		if err = s.approval(ctx, tx, r, domain.RunInput{
			Kind: domain.InputApproval, ApprovalID: a.ID, Decision: decision.Decision, Context: input.Context,
		}); err != nil {
			return err
		}
		return recordInteractionResolution(ctx, tx, &r, callID, decision, input)
	})
	return r, err
}

func validateResolution(r domain.Run, callID string, decision domain.InteractionResolution) error {
	if r.State != domain.Running || r.CancelRequested || r.PauseRequested {
		return domain.Conflict("stop_requested")
	}
	a := r.Approval
	if r.Interaction.ReplyMode != domain.ReplyModeAgent || a == nil || a.ReplyMode != domain.ReplyModeAgent ||
		a.Call == nil ||
		a.ID != decision.ApprovalID ||
		a.ArgumentHash != decision.ArgumentHash ||
		callID == "" {
		return domain.Conflict("interaction_conflict")
	}
	if decision.Decision != domain.DecisionApprove && decision.Decision != domain.DecisionReject {
		return domain.Conflict("invalid_decision")
	}
	return nil
}

func acceptedResolutionInput(
	ctx context.Context,
	tx pgx.Tx,
	r domain.Run,
	decision domain.InteractionResolution,
) (domain.RunInput, error) {
	if err := uuid.Validate(decision.InputID); err != nil {
		return domain.RunInput{}, domain.Conflict("input_conflict")
	}
	var raw []byte
	if err := tx.QueryRow(ctx, "SELECT doc FROM run_inputs WHERE id=$1 AND run_id=$2", decision.InputID, r.ID).
		Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.RunInput{}, domain.Conflict("input_conflict")
		}
		return domain.RunInput{}, err
	}
	var input domain.RunInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return domain.RunInput{}, err
	}
	if input.Kind != domain.InputSteering || input.Context == nil || input.Context.ApprovalID != r.Approval.ID ||
		input.Context.Revision != r.ConversationRevision {
		return domain.RunInput{}, domain.Conflict("consent_superseded")
	}
	if input.Presentation != nil && input.Presentation.ApprovalID != "" &&
		input.Presentation.ApprovalID != r.Approval.ID {
		return domain.RunInput{}, domain.Conflict("presentation_conflict")
	}
	if input.Presentation != nil && input.Presentation.QuestionID != "" &&
		input.Presentation.QuestionID != input.Context.QuestionID {
		return domain.RunInput{}, domain.Conflict("presentation_conflict")
	}
	return input, nil
}

func replayInputReceipt(ctx context.Context, tx pgx.Tx, r *domain.Run, inputID string) error {
	if inputID == r.ID {
		r.ConversationInput = nil // Legacy receipts stored the run ID.
		return nil
	}
	var raw []byte
	if err := tx.QueryRow(ctx, "SELECT doc FROM run_inputs WHERE id=$1 AND run_id=$2", inputID, r.ID).
		Scan(&raw); err != nil {
		return err
	}
	var accepted domain.RunInput
	if err := json.Unmarshal(raw, &accepted); err != nil {
		return err
	}
	r.ConversationInput = &accepted
	return nil
}

func (s *Store) recordConversationInput(
	ctx context.Context,
	tx pgx.Tx,
	p domain.Principal,
	r *domain.Run,
	in *domain.RunInput,
) (bool, error) {
	if in.Kind != domain.InputSteering ||
		(r.Interaction.ReplyMode != domain.ReplyModeAgent && (r.Approval == nil || r.Approval.ReplyMode != domain.ReplyModeExactText)) {
		return false, nil
	}
	r.ConversationRevision++
	in.Context = &domain.InputContext{Revision: r.ConversationRevision, SourceActor: actor(p)}
	if r.Approval != nil {
		in.Context.ApprovalID = r.Approval.ID
	}
	if r.Question != nil {
		in.Context.QuestionID = r.Question.ID
	}
	r.ConversationInput = in
	if a := r.Approval; a != nil && a.ReplyMode == domain.ReplyModeExactText && a.Decision == "" {
		if err := s.resolveExactText(ctx, tx, r, in); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(ctx, "UPDATE runs SET doc=$2 WHERE id=$1", r.ID, document(r)); err != nil {
		return false, err
	}
	return r.State == domain.WaitingInput || r.State == domain.WaitingApproval, nil
}

func (s *Store) resolveExactText(ctx context.Context, tx pgx.Tx, r *domain.Run, in *domain.RunInput) error {
	a := r.Approval
	if s.approval == nil {
		return domain.Conflict("approval_handler_unavailable")
	}
	decision := domain.DecisionReject
	if a.ExpiresAt.After(time.Now()) && in.Text == a.ConfirmationText && matchesPresentation(in, a.ID) {
		decision = domain.DecisionApprove
	}
	if err := s.approval(
		ctx,
		tx,
		*r,
		domain.RunInput{Kind: domain.InputApproval, ApprovalID: a.ID, Decision: decision, Context: in.Context},
	); err != nil {
		return err
	}
	a.Decision, a.DecisionInputID, a.ConsentRevision = decision, in.ID, r.ConversationRevision
	return runEvent(
		ctx,
		tx,
		r.ID,
		"interaction.resolved",
		map[string]string{"approval_id": a.ID, "input_id": in.ID, "decision": decision},
	)
}

func matchesPresentation(input *domain.RunInput, approvalID string) bool {
	p := input.Presentation
	if p == nil {
		return true
	}
	return (p.ApprovalID == "" || p.ApprovalID == approvalID) &&
		(p.QuestionID == "" || input.Context != nil && p.QuestionID == input.Context.QuestionID)
}

func recordInteractionResolution(
	ctx context.Context,
	tx pgx.Tx,
	r *domain.Run,
	callID string,
	decision domain.InteractionResolution,
	input domain.RunInput,
) error {
	a := r.Approval
	a.Decision, a.DecisionInputID, a.ResolutionCallID = decision.Decision, decision.InputID, callID
	a.ConsentRevision = input.Context.Revision
	if _, err := tx.Exec(ctx, "UPDATE runs SET doc=$2 WHERE id=$1", r.ID, document(r)); err != nil {
		return err
	}
	if _, err := chatEvent(
		ctx,
		tx,
		r.ChatID,
		r.ID,
		"interaction.resolved",
		decision.Decision,
		"agent",
		decision,
	); err != nil {
		return err
	}
	return runEvent(ctx, tx, r.ID, "interaction.resolved", decision)
}

func (s *Store) queueInput(ctx context.Context, tx pgx.Tx, r domain.Run) error {
	if _, err := tx.Exec(
		ctx,
		"UPDATE runs SET state='queued',reason='',doc=$2 WHERE id=$1",
		r.ID,
		document(r),
	); err != nil {
		return err
	}
	if s.enqueue != nil {
		return s.enqueue(ctx, tx, r.ID)
	}
	return nil
}

func replayResolution(a *domain.Approval, callID string, decision domain.InteractionResolution) error {
	if a.ResolutionCallID != callID || a.DecisionInputID != decision.InputID || a.Decision != decision.Decision {
		return domain.Conflict("interaction_resolved")
	}
	return nil
}
