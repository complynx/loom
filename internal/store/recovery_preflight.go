package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
)

// Check successor prerequisites before requesting cancellation. The predecessor's
// own reservation is intentionally excluded; final funds/capacity checks run again.
func (s *Store) preflightRecoveryReplacement(
	ctx context.Context,
	tx pgx.Tx,
	p domain.Principal,
	uid string,
	in domain.RecoverMessage,
	r domain.Run,
) error {
	if r.State != domain.Running {
		return nil
	}
	if r.MaxMicrocredits-r.SpentMicrocredits <= 0 {
		return domain.NewError(http.StatusTooManyRequests, "budget_exhausted")
	}
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
		recoveryRunCreation(r, in.Text),
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
// would meet now. It uses the same checks as message delivery.
func locateRecoveryBlocker(
	ctx context.Context,
	tx pgx.Tx,
	uid string,
	r domain.Run,
	out *domain.RecoveryReceipt,
) error {
	terminal := r.State.Terminal()
	if terminal {
		out.NextAction = "send_message"
	}
	// A message starts a new round after a terminal run and steers any other run.
	blocked, err := recoveryDeliveryBlocked(ctx, tx, uid, r, false, out)
	if err != nil || blocked || terminal {
		return err
	}
	// The next message wakes a run paused at its step limit.
	wakes, err := stepLimitWakes(ctx, tx, r)
	if wakes {
		out.Status, out.NextAction = "accepted", "send_message"
	}
	return err
}
