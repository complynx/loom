package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
)

const (
	// Caller keys use the first two routes. Recovery uses its own key spaces, so
	// a caller key can neither block nor satisfy the delivery of an inbox message.
	runIdempotencyRoute   = "runs"
	inputIdempotencyRoute = "input"
	recoveryRunRoute      = "recovery-runs"
	recoveryInputRoute    = "recovery-input"

	uncertainEffectReason      = "inference_or_effect_uncertain"
	recoveryPendingStatus      = "recovery_pending"
	recoveryCancelCommand      = "cancel"
	recoveryRetryAction        = "retry_same_message"
	recoveryCancellationReason = "cancellation_pending"
	currentChatRunQuery        = "SELECT " + runColumns + " FROM runs WHERE chat_id=$1 ORDER BY (state NOT IN('completed','cancelled','failed')) DESC,COALESCE((doc->>'admission_sequence')::bigint,0) DESC,id DESC LIMIT 1"
)

// ActiveChatRun locates retained work without requiring a consumer's local pointer.
func (s *Store) ActiveChatRun(ctx context.Context, p domain.Principal, cid string) (domain.RecoveryReceipt, error) {
	var out domain.RecoveryReceipt
	if !p.Has("chat.read") {
		return out, domain.Forbidden()
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var uid string
		if err := tx.QueryRow(ctx, "SELECT id::text FROM users WHERE issuer=$1 AND subject=$2", p.Issuer, p.Subject).
			Scan(&uid); err != nil {
			return err
		}
		if err := checkChat(ctx, tx, uid, p.OrgID, cid); err != nil {
			return err
		}
		r, err := scanRun(
			tx.QueryRow(
				ctx,
				currentChatRunQuery,
				cid,
			),
		)
		if errors.Is(err, domain.ErrNotFound) {
			out.Status, out.NextAction = "idle", "create_run"
			return nil
		}
		if err != nil {
			return err
		}
		if r.ActorID != actor(p) {
			return domain.NotFound()
		}
		out = recoveryRunReceipt(r)
		return locateRecoveryBlocker(ctx, tx, uid, r, &out)
	})
	return out, err
}

// RecoverChatMessage retains input before attempting delivery. A retry advances
// the same handoff; no background loop creates runs or spends credits.
func (s *Store) RecoverChatMessage(
	ctx context.Context,
	p domain.Principal,
	cid, key string,
	in domain.RecoverMessage,
) (domain.RecoveryReceipt, error) {
	var out domain.RecoveryReceipt
	if !p.Has("chat.read") || !p.Has("runs.control") || !p.Has("runs.create") {
		return out, domain.Forbidden()
	}
	if key == "" || len(key) > 128 || strings.TrimSpace(in.Text) == "" || len(in.Text) > 65536 {
		return out, domain.NewError(http.StatusUnprocessableEntity, "invalid_message")
	}
	if _, err := uuid.Parse(in.ReplaceRunID); in.ReplaceRunID != "" && err != nil {
		return out, domain.NewError(http.StatusUnprocessableEntity, "invalid_run_id")
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		out = domain.RecoveryReceipt{}
		uid, err := s.ensureUser(ctx, tx, p)
		if err != nil {
			return err
		}
		var live string
		if err = tx.QueryRow(ctx, "SELECT id::text FROM chats WHERE id=$1 AND owner_id=$2 AND org_id=$3 AND deleted_at IS NULL FOR UPDATE", cid, uid, p.OrgID).
			Scan(&live); err != nil {
			return err
		}
		if err = receiveRecoveryMessage(ctx, tx, p, uid, cid, key, in, &out); err != nil {
			return err
		}
		out.Accepted = true
		done, replayErr := replayRecoveryMessage(ctx, tx, &out)
		if replayErr != nil || done {
			return replayErr
		}
		blocked, blockErr := earlierRecoveryMessage(ctx, tx, cid, actor(p), &out)
		if blockErr != nil || blocked {
			return blockErr
		}
		return s.attemptRecoveryDelivery(ctx, tx, p, uid, cid, in, &out)
	})
	return out, err
}

// blockRecovery reports a blocker that holds a retained message or a terminal
// predecessor. One reason always carries one next_action on every endpoint.
func blockRecovery(out *domain.RecoveryReceipt, reason string) {
	action := "resolve_blocker_then_retry"
	switch reason {
	case "credits_exhausted":
		action = "fund_wallet_then_retry"
	case "budget_exhausted", "replacement_limit", "run_order_unavailable":
		action = "request_new_work_allowance"
	case "authorization_required":
		action = "reauthorize_session"
	case "model_not_allowed":
		action = "select_allowed_model_or_restore_access"
	case uncertainEffectReason:
		action = "reconcile_predecessor"
	case "replacement_target_changed":
		action = "abandon_message_or_inspect_run"
	case "earlier_message_pending":
		action = "retry_earlier_message"
	case "other_actor_message_pending":
		action = "contact_original_actor"
	}
	out.Status, out.Reason, out.NextAction = "blocked", reason, action
}

func recoveryRunReceipt(r domain.Run) domain.RecoveryReceipt {
	out := domain.RecoveryReceipt{RunID: r.ID, Status: "accepted", Reason: r.Reason, NextAction: "read_run"}
	switch {
	case r.State.Terminal():
		out.Status, out.NextAction = "terminal", "read_run"
	case r.CancelRequested:
		out.Status, out.NextAction = recoveryPendingStatus, recoveryRetryAction
	case r.State == domain.Queued || r.State == domain.Running:
		out.Status = "queued"
	case r.State == domain.WaitingAuth:
		blockRecovery(&out, "authorization_required")
	case r.State == domain.Reconciling:
		blockRecovery(&out, uncertainEffectReason)
	case r.State == domain.Paused:
		// A paused run keeps its own pause reason. Its action is to resume the run.
		out.Status, out.NextAction = "blocked", "resolve_blocker_then_resume"
	}
	return out
}

func setRecoveryRun(out *domain.RecoveryReceipt, r domain.Run) {
	v := recoveryRunReceipt(r)
	out.RunID, out.Status, out.Reason, out.NextAction = v.RunID, v.Status, v.Reason, v.NextAction
}

func (s *Store) deliverRecoveryMessage(
	ctx context.Context,
	tx pgx.Tx,
	p domain.Principal,
	uid, cid string,
	in domain.RecoverMessage,
	out *domain.RecoveryReceipt,
) error {
	r, err := scanRun(
		tx.QueryRow(
			ctx,
			currentChatRunQuery+" FOR UPDATE",
			cid,
		),
	)
	if err != nil {
		return err
	}
	if r.ActorID != actor(p) {
		return domain.Forbidden()
	}
	out.RunID = r.ID
	replace := in.ReplaceRunID == r.ID || r.State.Terminal()
	if replace {
		if preflightErr := s.preflightRecoveryReplacement(ctx, tx, p, uid, in, r); preflightErr != nil {
			return preflightErr
		}
		pending, pendingErr := recoveryReplacementPending(ctx, tx, r, out)
		if pendingErr != nil || pending {
			return pendingErr
		}
	}
	blocked, err := recoveryDeliveryBlocked(ctx, tx, uid, r, replace, out)
	if err != nil || blocked {
		return err
	}

	if in.ReplaceRunID != "" && in.ReplaceRunID != r.ID {
		// A racing retry may already have created the direct successor.
		if r.ReplacesRunID != in.ReplaceRunID {
			return domain.Conflict("replacement_target_changed")
		}
	}
	delivered := true
	if replace {
		r, err = s.createRecoverySuccessor(ctx, tx, p, uid, cid, in, r, out.MessageID)
	} else {
		r, delivered, err = s.steerRecoveryRun(ctx, tx, p, in, r, out)
	}
	if err != nil || !delivered {
		return err
	}
	setRecoveryRun(out, r)
	out.Delivered = true
	_, err = tx.Exec(
		ctx,
		"UPDATE conversation_messages SET delivered=true,run_id=$2,input_id=NULLIF($3,'')::uuid WHERE id=$1",
		out.MessageID,
		r.ID,
		out.InputID,
	)
	return err
}

func receiveRecoveryMessage(
	ctx context.Context,
	tx pgx.Tx,
	p domain.Principal,
	uid, cid, key string,
	in domain.RecoverMessage,
	out *domain.RecoveryReceipt,
) error {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT id::text,seq,request,COALESCE(run_id::text,''),COALESCE(input_id::text,''),delivered,abandoned FROM conversation_messages WHERE chat_id=$1 AND owner_id=$2 AND org_id=$3 AND actor_id=$4 AND key=$5`, cid, uid, p.OrgID, actor(p), key).
		Scan(&out.MessageID, &out.Sequence, &raw, &out.RunID, &out.InputID, &out.Delivered, &out.Abandoned)
	if errors.Is(err, pgx.ErrNoRows) {
		return insertRecoveryMessage(ctx, tx, p, uid, cid, key, in, out)
	}
	if err != nil {
		return err
	}
	var original domain.RecoverMessage
	if err = json.Unmarshal(raw, &original); err != nil {
		return err
	}
	if original != in {
		return domain.Conflict("idempotency_conflict")
	}
	return nil
}

func insertRecoveryMessage(
	ctx context.Context,
	tx pgx.Tx,
	p domain.Principal,
	uid, cid, key string,
	in domain.RecoverMessage,
	out *domain.RecoveryReceipt,
) error {
	r, err := scanRun(tx.QueryRow(ctx, currentChatRunQuery, cid))
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Conflict("no_run")
	}
	if err != nil {
		return err
	}
	if r.ActorID != actor(p) {
		return domain.NotFound()
	}
	if in.ReplaceRunID != "" && in.ReplaceRunID != r.ID && in.ReplaceRunID != r.ReplacesRunID {
		return domain.Conflict("replacement_target_changed")
	}
	out.MessageID = id()
	return tx.QueryRow(ctx, `INSERT INTO conversation_messages(id,chat_id,owner_id,org_id,actor_id,key,request) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING seq`, out.MessageID, cid, uid, p.OrgID, actor(p), key, document(in)).
		Scan(&out.Sequence)
}

func (s *Store) attemptRecoveryDelivery(
	ctx context.Context,
	tx pgx.Tx,
	p domain.Principal,
	uid, cid string,
	in domain.RecoverMessage,
	out *domain.RecoveryReceipt,
) error {
	// Roll back admission/input changes on a policy denial, but commit the inbox.
	attempt, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	err = s.deliverRecoveryMessage(ctx, attempt, p, uid, cid, in, out)
	if err == nil {
		return attempt.Commit(ctx)
	}
	if rollbackErr := attempt.Rollback(ctx); rollbackErr != nil {
		return rollbackErr
	}
	var denied *domain.Error
	if !errors.As(err, &denied) {
		return err
	}
	out.RunID, out.InputID = "", ""
	blockRecovery(out, denied.Code)
	return nil
}

func (s *Store) createRecoverySuccessor(
	ctx context.Context,
	tx pgx.Tx,
	p domain.Principal,
	uid, cid string,
	in domain.RecoverMessage,
	r domain.Run,
	messageID string,
) (domain.Run, error) {
	if !r.State.Terminal() {
		if terminationErr := terminateLifecycleRun(ctx, tx, r.ID, "cancelled", "superseded"); terminationErr != nil {
			return domain.Run{}, terminationErr
		}
	}
	settings, prefs, policy, err := s.admissionSettings(ctx, tx, uid, p.OrgID)
	if err != nil {
		return domain.Run{}, err
	}
	if err = validateRecoverySession(ctx, tx, p, r.AuthSessionID); err != nil {
		return domain.Run{}, err
	}
	creation := recoveryRunCreation(r, in.Text)
	next, err := buildRecoveryRun(p, uid, cid, creation, settings, prefs, policy)
	if err != nil {
		return domain.Run{}, err
	}
	next.ReplacesRunID, next.ReplacementCount = r.ID, r.ReplacementCount+1
	// Record the replacement before the successor's request. Model context then
	// shows it as earlier history, not as input after the current request.
	if _, err = chatEvent(
		ctx,
		tx,
		cid,
		r.ID,
		"run.replaced",
		"",
		"system",
		map[string]string{"predecessor_run_id": r.ID, "successor_run_id": next.ID},
	); err != nil {
		return domain.Run{}, err
	}
	if err = s.persistRun(ctx, tx, p, recoveryRunRoute, messageID, creation, next); err != nil {
		return domain.Run{}, err
	}
	// Terminal predecessors also lose any stale executor lease.
	if _, err = tx.Exec(
		ctx,
		"UPDATE runs SET fence=fence+1,lease_owner='',lease_until=NULL WHERE id=$1",
		r.ID,
	); err != nil {
		return domain.Run{}, err
	}
	return next, nil
}

func recoveryDeliveryBlocked(
	ctx context.Context,
	tx pgx.Tx,
	uid string,
	r domain.Run,
	replace bool,
	out *domain.RecoveryReceipt,
) (bool, error) {
	// Never move work across a predecessor with unknown financial/business effects.
	if r.State.Terminal() && r.AdmissionSequence == 0 {
		blockRecovery(out, "run_order_unavailable")
		return true, nil
	}
	uncertain, err := runUncertainty(ctx, tx, r.ID)
	if err != nil {
		return false, err
	}
	if replace {
		var sponsoredUnknown bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM platform_provider_attempts WHERE run_id=$1 AND status='unknown')", r.ID).
			Scan(&sponsoredUnknown); err != nil {
			return false, err
		}
		uncertain = uncertain || sponsoredUnknown
	}
	if replace && (uncertain || r.ReservedMicrocredits > 0) {
		blockRecovery(out, uncertainEffectReason)
		return true, nil
	}
	var available int64
	if err = tx.QueryRow(ctx, "SELECT balance-reserved FROM billing_accounts WHERE owner_id=$1", uid).
		Scan(&available); err != nil {
		return false, err
	}
	if !replace {
		available += r.ReservedMicrocredits
	}
	if available <= 0 {
		blockRecovery(out, "credits_exhausted")
		return true, nil
	}
	if r.MaxMicrocredits-r.SpentMicrocredits <= 0 {
		blockRecovery(out, "budget_exhausted")
		return true, nil
	}
	return false, nil
}

func recoveryReplacementPending(
	ctx context.Context,
	tx pgx.Tx,
	r domain.Run,
	out *domain.RecoveryReceipt,
) (bool, error) {
	var err error
	if r.ReplacementCount >= domain.MaxConversationReplacements {
		blockRecovery(out, "replacement_limit")
		return true, nil
	}
	if r.State == domain.Running {
		if r.CancelRequested {
			out.Status, out.Reason, out.NextAction = recoveryPendingStatus, recoveryCancellationReason, recoveryRetryAction
			return true, nil
		}
		if _, err = tx.Exec(ctx, "UPDATE runs SET cancel_requested=true WHERE id=$1", r.ID); err != nil {
			return false, err
		}
		if err = runEvent(
			ctx,
			tx,
			r.ID,
			"run.status",
			map[string]string{"command": recoveryCancelCommand, "reason": "conversation_recovery"},
		); err != nil {
			return false, err
		}
		out.Status, out.Reason, out.NextAction = recoveryPendingStatus, recoveryCancellationReason, recoveryRetryAction
		return true, nil
	}

	return false, nil
}

func earlierRecoveryMessage(
	ctx context.Context,
	tx pgx.Tx,
	cid, actorID string,
	out *domain.RecoveryReceipt,
) (bool, error) {
	var blockerActor string
	err := tx.QueryRow(ctx, "SELECT id::text,actor_id FROM conversation_messages WHERE chat_id=$1 AND seq<$2 AND NOT delivered AND NOT abandoned ORDER BY seq LIMIT 1", cid, out.Sequence).
		Scan(&out.BlockingMessageID, &blockerActor)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if blockerActor != actorID {
		out.BlockingMessageID = ""
		blockRecovery(out, "other_actor_message_pending")
		return true, nil
	}
	blockRecovery(out, "earlier_message_pending")
	return true, nil
}

func (s *Store) steerRecoveryRun(
	ctx context.Context,
	tx pgx.Tx,
	p domain.Principal,
	in domain.RecoverMessage,
	r domain.Run,
	out *domain.RecoveryReceipt,
) (domain.Run, bool, error) {
	var err error
	if r.CancelRequested {
		out.Status, out.Reason, out.NextAction = recoveryPendingStatus, recoveryCancellationReason, recoveryRetryAction
		return r, false, nil
	}
	if err = s.addInput(
		ctx,
		tx,
		p,
		r.ID,
		recoveryInputRoute,
		out.MessageID,
		domain.RunInput{Kind: domain.InputSteering, Text: in.Text},
		&r,
	); err != nil {
		return r, false, err
	}
	if r.ConversationInput != nil {
		out.InputID = r.ConversationInput.ID
	}
	return r, true, nil
}

func replayRecoveryMessage(ctx context.Context, tx pgx.Tx, out *domain.RecoveryReceipt) (bool, error) {
	if out.Abandoned {
		abandonedReceipt(out)
		return true, nil
	}
	if out.Delivered {
		r, e := scanRun(tx.QueryRow(ctx, "SELECT "+runColumns+" FROM runs WHERE id=$1", out.RunID))
		if e != nil {
			return false, e
		}
		setRecoveryRun(out, r)
		return true, nil
	}

	return false, nil
}

func validateRecoverySession(ctx context.Context, tx pgx.Tx, p domain.Principal, sid string) error {
	var err error
	if sid != "" {
		var valid bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM auth_sessions WHERE id=$1 AND issuer=$2 AND subject=$3 AND actor=$4 AND org_id=$5 AND revoked_at IS NULL AND expires_at>now())", sid, p.Issuer, p.Subject, actor(p), p.OrgID).
			Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return domain.NewError(http.StatusUnauthorized, "authorization_required")
		}
	}
	return nil
}

func recoveryRunCreation(r domain.Run, text string) domain.CreateRun {
	return domain.CreateRun{
		ChatID:           r.ChatID,
		AuthSessionID:    r.AuthSessionID,
		Input:            domain.Input{Text: text},
		Interaction:      r.Interaction,
		ExecutionProfile: r.ExecutionProfile,
		OutputPolicy:     &r.OutputPolicy,
		Skills:           r.Skills,
		Discovery:        r.Discovery,
		DisconnectPolicy: r.DisconnectPolicy,
		Budget:           domain.Budget{MaxMicrocredits: r.MaxMicrocredits - r.SpentMicrocredits},
	}
}
