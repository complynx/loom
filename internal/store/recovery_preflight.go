package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
)

// preflightRecoveryReplacement checks successor prerequisites before it requests
// the cancellation of running work. The reservation of the predecessor returns to
// the wallet when it stops, so it counts as available here. Funds and capacity
// are checked again when the successor is admitted.
func (s *Store) preflightRecoveryReplacement(
	ctx context.Context,
	tx pgx.Tx,
	p domain.Principal,
	uid string,
	r domain.Run,
) error {
	if r.AdmissionSequence == 0 {
		return domain.Conflict("run_order_unavailable")
	}
	if r.State != domain.Running {
		return nil
	}
	var available int64
	if err := tx.QueryRow(ctx, "SELECT balance-reserved FROM billing_accounts WHERE owner_id=$1", uid).
		Scan(&available); err != nil {
		return err
	}
	if available+r.ReservedMicrocredits <= 0 {
		return domain.NewError(http.StatusTooManyRequests, creditsExhaustedReason)
	}
	return s.checkSuccessorAdmission(ctx, tx, p, uid, r)
}

// checkSuccessorAdmission checks the saved session and the current model
// entitlement for a run that would follow r. It changes nothing.
func (s *Store) checkSuccessorAdmission(
	ctx context.Context,
	tx pgx.Tx,
	p domain.Principal,
	uid string,
	r domain.Run,
) error {
	if err := validateRecoverySession(ctx, tx, p, r.AuthSessionID); err != nil {
		return err
	}
	settings, prefs, policy, _, err := readRunSettings(ctx, tx, uid, p.OrgID)
	if err != nil {
		return err
	}
	_, err = buildRecoveryRun(
		p,
		uid,
		r.ChatID,
		recoveryRunCreation(r, ""),
		s.normalizeExecutionSettings(settings),
		prefs,
		policy,
	)
	return err
}

func buildRecoveryRun(
	p domain.Principal,
	uid, cid string,
	in domain.CreateRun,
	settings domain.Settings,
	prefs domain.Preferences,
	policy domain.UserPolicy,
) (domain.Run, error) {
	r, err := buildRun(p, uid, cid, in, settings, prefs, policy)
	if denial, ok := errors.AsType[*domain.Error](
		err,
	); ok && denial.Status == http.StatusForbidden &&
		denial.Code == "forbidden" {
		return domain.Run{}, domain.NewError(http.StatusForbidden, "model_not_allowed")
	}
	return r, err
}

func checkPendingConversationMessages(ctx context.Context, tx pgx.Tx, cid string) error {
	var pending bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM conversation_messages WHERE chat_id=$1 AND NOT delivered AND NOT abandoned)", cid).
		Scan(&pending); err != nil {
		return err
	}
	if pending {
		return domain.Conflict("pending_conversation_messages")
	}
	return nil
}

// locateRecoveryBlocker makes the locator report the blocker that a message
// would meet now. It uses the same checks as message delivery, except the run
// capacity limits, which need the admission lock.
func (s *Store) locateRecoveryBlocker(
	ctx context.Context,
	tx pgx.Tx,
	p domain.Principal,
	uid string,
	r domain.Run,
	out *domain.RecoveryReceipt,
) error {
	terminal := r.State.Terminal()
	// A message without a target replaces only a run that spent its own allowance.
	replace, err := allowanceSpent(ctx, tx, r)
	if err != nil {
		return err
	}
	blocked, err := recoveryDeliveryBlocked(ctx, tx, uid, r, replace, out)
	if err != nil || blocked {
		return err
	}
	if terminal || replace {
		// The message would admit a new run.
		admissionErr := s.checkLocatedSuccessor(ctx, tx, p, uid, r, replace)
		if denial, denied := errors.AsType[*domain.Error](admissionErr); denied {
			blockRecovery(out, denial.Code)
			return nil
		}
		if admissionErr != nil {
			return admissionErr
		}
		out.NextAction = recoverySendAction
		if replace {
			// The run used its own allowance without a result. The user decides
			// whether to continue; the wallet has funds.
			out.Status, out.NextAction = recoveryAcceptedStatus, recoveryAskUserAction
		}
		return nil
	}
	// The next message wakes a run paused at its step limit or for an empty wallet.
	if continuationErr := checkRecoveryContinuation(ctx, tx, p, r); continuationErr != nil {
		if denial, denied := errors.AsType[*domain.Error](continuationErr); denied {
			blockRecovery(out, denial.Code)
			return nil
		}
		return continuationErr
	}
	wakes, err := inputWakes(ctx, tx, r)
	if !wakes {
		return err
	}
	out.Status, out.NextAction = recoveryAcceptedStatus, recoverySendAction
	if r.Reason == stepLimitReason {
		// The run worked without a result. The user decides whether to continue.
		out.NextAction = recoveryAskUserAction
	}
	return err
}

func (s *Store) checkLocatedSuccessor(
	ctx context.Context,
	tx pgx.Tx,
	p domain.Principal,
	uid string,
	r domain.Run,
	replace bool,
) error {
	if replace {
		if r.AdmissionSequence == 0 {
			return domain.Conflict("run_order_unavailable")
		}
		if _, err := captureRecoveryReceiptIDs(ctx, tx, r); err != nil {
			return err
		}
	}
	return s.checkSuccessorAdmission(ctx, tx, p, uid, r)
}

// Check the immutable existing model, independently of successor preferences.
func checkRecoveryContinuation(ctx context.Context, tx pgx.Tx, p domain.Principal, r domain.Run) error {
	if err := validateRecoverySession(ctx, tx, p, r.AuthSessionID); err != nil {
		return err
	}
	settings, _, policy, _, err := readRunSettings(ctx, tx, r.OwnerID, r.OrgID)
	if err != nil {
		return err
	}
	models := settings.Models
	if policy.Models != nil {
		models = effectiveModels(settings.Models, policy.Models)
	}
	for _, model := range models {
		if model.ID == r.ModelID && allowedEffort(model, r.Effort) && model.Provider == r.Model.Provider &&
			model.ProviderModel == r.Model.ProviderModel {
			return nil
		}
	}
	return domain.NewError(http.StatusForbidden, "model_not_allowed")
}
