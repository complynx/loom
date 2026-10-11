package store

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
)

func (s *Store) ClaimRun(ctx context.Context, rid, owner string, duration time.Duration) (domain.Run, error) {
	var r domain.Run
	err := s.tx(ctx, func(tx pgx.Tx) error {
		return s.claimRun(ctx, tx, rid, owner, duration, &r)
	})
	if err == nil && r.State != domain.Running {
		return r, domain.Conflict("inference_uncertain")
	}
	return r, err
}
func leaseRun(ctx context.Context, tx pgx.Tx, rid, owner string, fence int64) (domain.Run, error) {
	r, e := scanRun(
		tx.QueryRow(
			ctx,
			"SELECT "+runColumns+" FROM runs WHERE id=$1 AND lease_owner=$2 AND fence=$3 AND lease_until>clock_timestamp() FOR UPDATE",
			rid,
			owner,
			fence,
		),
	)
	if e != nil {
		return r, domain.Conflict("lease_lost")
	}
	return r, nil
}
func (s *Store) Heartbeat(ctx context.Context, rid, owner string, fence int64, duration time.Duration) error {
	tag, e := s.pool.Exec(
		ctx,
		"UPDATE runs SET lease_until=clock_timestamp()+$4*interval '1 second' WHERE id=$1 AND lease_owner=$2 AND fence=$3 AND lease_until>clock_timestamp() AND state='running'",
		rid,
		owner,
		fence,
		duration.Seconds(),
	)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return domain.Conflict("lease_lost")
	}
	return nil
}
func (s *Store) ExecutionModel(ctx context.Context, rid string) (domain.Model, error) {
	r, e := s.ExecutionRun(ctx, rid)
	if e != nil {
		return domain.Model{}, e
	}
	policy, e := s.policy(ctx, r.OwnerID, r.OrgID)
	if e != nil {
		return domain.Model{}, e
	}
	for _, current := range policy.Models {
		if current.ID != r.ModelID || !allowedEffort(current, r.Effort) {
			continue
		}
		if current.ProviderModel != r.Model.ProviderModel || current.Provider != r.Model.Provider {
			return domain.Model{}, domain.Conflict("model_contract_changed")
		}
		model := r.Model
		model.SupportsImages = model.SupportsImages && current.SupportsImages
		if current.MaxOutputTokens < model.MaxOutputTokens {
			model.MaxOutputTokens = current.MaxOutputTokens
		}
		if current.ContextTokens > 0 && (model.ContextTokens == 0 || current.ContextTokens < model.ContextTokens) {
			model.ContextTokens = current.ContextTokens
		}
		return model, nil
	}
	return domain.Model{}, domain.Forbidden()
}

func (s *Store) BeginStep(
	ctx context.Context,
	rid, owner string,
	fence int64,
	kind string,
	input json.RawMessage,
	reserve int64,
) (domain.Step, error) {
	var step domain.Step
	if reserve < 0 {
		return step, domain.NewError(http.StatusUnprocessableEntity, "invalid_reservation")
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		r, e := leaseRun(ctx, tx, rid, owner, fence)
		if e != nil {
			return e
		}
		if r.State != domain.Running || r.PauseRequested || r.CancelRequested {
			return domain.Conflict("stop_requested")
		}
		var pending bool
		if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM steps WHERE run_id=$1 AND state IN('pending','unknown'))", rid).
			Scan(&pending); e != nil {
			return e
		}
		if pending {
			r.State = domain.Reconciling
			return domain.Conflict("inference_uncertain")
		}
		if reserve > r.MaxMicrocredits-r.SpentMicrocredits-r.ReservedMicrocredits {
			return domain.NewError(http.StatusTooManyRequests, "budget_exhausted")
		}
		tag, e := tx.Exec(
			ctx,
			"UPDATE billing_accounts SET reserved=reserved+$2 WHERE owner_id=$1 AND balance-reserved>=$2",
			r.OwnerID,
			reserve,
		)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return domain.NewError(http.StatusTooManyRequests, "credits_exhausted")
		}
		step = domain.Step{ID: id(), RunID: rid, Kind: kind, State: stepPending, Input: input}
		e = tx.QueryRow(ctx, "INSERT INTO steps(id,run_id,ordinal,kind,state,input,reserve) SELECT $1,$2,COALESCE(max(ordinal),0)+1,$3,'pending',$4,$5 FROM steps WHERE run_id=$2 RETURNING ordinal,created_at", step.ID, rid, kind, input, reserve).
			Scan(&step.Ordinal, &step.CreatedAt)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, "UPDATE runs SET reserved_microcredits=reserved_microcredits+$2 WHERE id=$1", rid, reserve)
		if e != nil {
			return e
		}
		_, e = tx.Exec(
			ctx,
			"INSERT INTO credit_ledger(id,owner_id,run_id,step_id,settlement_key,amount,kind) VALUES($1,$2,$3,$4,$5,$6,'reserve')",
			id(),
			r.OwnerID,
			rid,
			step.ID,
			"reserve:"+step.ID,
			reserve,
		)
		return e
	})
	return step, err
}

func (s *Store) CompleteStep(
	ctx context.Context,
	rid, owner string,
	fence int64,
	sid string,
	result json.RawMessage,
	usage domain.Usage,
) error {
	if usage.Microcredits < 0 || usage.InputTokens < 0 || usage.CachedInputTokens < 0 ||
		usage.CachedInputTokens > usage.InputTokens ||
		usage.CacheWriteTokens < 0 ||
		usage.CacheWriteTokens > usage.InputTokens-usage.CachedInputTokens ||
		usage.OutputTokens < 0 {
		return domain.NewError(http.StatusUnprocessableEntity, "invalid_usage")
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		return s.completeStep(ctx, tx, rid, owner, fence, sid, result, usage)
	})
}
func (s *Store) MarkStepUnknown(ctx context.Context, rid, owner string, fence int64, sid string) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		r, e := leaseRun(ctx, tx, rid, owner, fence)
		if e != nil {
			return e
		}
		tag, e := tx.Exec(
			ctx,
			"UPDATE steps SET state='unknown' WHERE id=$1 AND run_id=$2 AND state='pending'",
			sid,
			rid,
		)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return domain.Conflict("step_conflict")
		}
		state := domain.Reconciling
		reason := "inference_uncertain"
		if limit := r.SettingsSnapshot.RunActiveTimeSeconds; limit > 0 && r.ActiveMilliseconds >= int64(limit)*1000 {
			state = domain.Failed
			reason = "active_time_expired"
		}
		lifetime := r.SettingsSnapshot.RunLifetimeSeconds
		if lifetime == 0 {
			lifetime = 604800
		}
		if time.Now().After(r.CreatedAt.Add(time.Duration(lifetime) * time.Second)) {
			state = domain.Failed
			reason = "run_expired"
		}
		if r.CancelRequested {
			state = domain.Cancelled
			reason = clientCancelReason
		}
		_, e = tx.Exec(
			ctx,
			"UPDATE runs SET state=$2,reason=$3,lease_owner='',lease_until=NULL WHERE id=$1",
			rid,
			state,
			reason,
		)
		if e != nil {
			return e
		}
		return runEvent(ctx, tx, rid, "run.status", map[string]any{eventStatusKey: state, eventReasonKey: reason})
	})
}
func (s *Store) ExecutionSteps(ctx context.Context, rid string) ([]domain.Step, error) {
	rows, e := s.pool.Query(
		ctx,
		"SELECT id::text,run_id::text,ordinal,kind,state,input,result,usage,created_at FROM steps WHERE run_id=$1 AND state='completed' ORDER BY ordinal",
		rid,
	)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []domain.Step{}
	for rows.Next() {
		var v domain.Step
		var raw []byte
		if e = rows.Scan(
			&v.ID,
			&v.RunID,
			&v.Ordinal,
			&v.Kind,
			&v.State,
			&v.Input,
			&v.Result,
			&raw,
			&v.CreatedAt,
		); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &v.Usage); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) Transition(
	ctx context.Context,
	rid, owner string,
	fence int64,
	state domain.RunState,
	reason, result string,
) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		return s.transition(ctx, tx, rid, owner, fence, state, reason, result)
	})
}
func (s *Store) AskQuestion(ctx context.Context, rid, owner string, fence int64, q domain.Question) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		return s.askQuestion(ctx, tx, rid, owner, fence, q)
	})
}
func (s *Store) PendingInputs(ctx context.Context, rid, owner string, fence int64) ([]domain.RunInput, error) {
	r, e := s.ExecutionRun(ctx, rid)
	if e != nil {
		return nil, e
	}
	if r.LeaseOwner != owner || r.Fence != fence {
		return nil, domain.Conflict("lease_lost")
	}
	rows, e := s.pool.Query(
		ctx,
		"SELECT id::text,doc FROM run_inputs WHERE run_id=$1 AND NOT applied ORDER BY created_at,id",
		rid,
	)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []domain.RunInput{}
	for rows.Next() {
		var v domain.RunInput
		var raw []byte
		if e = rows.Scan(&v.ID, &raw); e != nil {
			return nil, e
		}
		uid := v.ID
		if e = json.Unmarshal(raw, &v); e != nil {
			return nil, e
		}
		v.ID = uid
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) MarkInputsApplied(ctx context.Context, rid, owner string, fence int64, ids []string) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		if _, e := leaseRun(ctx, tx, rid, owner, fence); e != nil {
			return e
		}
		for _, uid := range ids {
			tag, e := tx.Exec(
				ctx,
				"UPDATE run_inputs SET applied=true WHERE id=$1 AND run_id=$2 AND NOT applied",
				uid,
				rid,
			)
			if e != nil {
				return e
			}
			if tag.RowsAffected() > 0 {
				if e = runEvent(ctx, tx, rid, "input.applied", map[string]string{"input_id": uid}); e != nil {
					return e
				}
			}
		}
		return nil
	})
}
func (s *Store) Control(ctx context.Context, p domain.Principal, rid, key, command string) (domain.Run, error) {
	var r domain.Run
	if !p.Has("runs.control") {
		return r, domain.Forbidden()
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		return s.control(ctx, tx, p, rid, key, command, &r)
	})
	return mutationReceipt(p, r), err
}

func (s *Store) AddInput(
	ctx context.Context,
	p domain.Principal,
	rid, key string,
	in domain.RunInput,
) (domain.Run, error) {
	var r domain.Run
	if !p.Has("runs.control") {
		return r, domain.Forbidden()
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		return s.addInput(ctx, tx, p, rid, key, in, &r)
	})
	return mutationReceipt(p, r), err
}
func inWithoutID(in domain.RunInput) domain.RunInput { in.ID = ""; return in }

func (s *Store) AskApproval(ctx context.Context, rid, owner string, fence int64, a domain.Approval) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		return s.askApproval(ctx, tx, rid, owner, fence, a)
	})
}

func mutationReceipt(p domain.Principal, r domain.Run) domain.Run {
	if p.Has("chat.read") {
		return r
	}
	return domain.Run{ID: r.ID, State: r.State, Reason: r.Reason, Version: r.Version}
}

func cancelInteraction(ctx context.Context, tx pgx.Tx, r domain.Run) error {
	if _, err := tx.Exec(
		ctx,
		`UPDATE runs SET state='cancelled',reason='client_cancel',lease_owner='',lease_until=NULL WHERE id=$1`,
		r.ID,
	); err != nil {
		return err
	}
	return runEvent(
		ctx,
		tx,
		r.ID,
		"run.status",
		map[string]any{eventStatusKey: domain.Cancelled, eventReasonKey: clientCancelReason},
	)
}

func runUncertainty(ctx context.Context, tx pgx.Tx, rid string) (bool, error) {
	var uncertain bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM steps WHERE run_id=$1 AND state IN('pending','unknown')) OR EXISTS(SELECT 1 FROM write_operations WHERE run_id=$1 AND state IN('dispatching','pending','unknown'))`, rid).
		Scan(&uncertain)
	return uncertain, err
}

func (s *Store) claimRun(
	ctx context.Context,
	tx pgx.Tx,
	rid, owner string,
	duration time.Duration,
	r *domain.Run,
) error {
	var e error
	*r, e = scanRun(tx.QueryRow(ctx, "SELECT "+runColumns+" FROM runs WHERE id=$1 FOR UPDATE", rid))
	if e != nil {
		return e
	}
	if r.State.Terminal() || r.State != domain.Queued && r.State != domain.Running {
		return domain.Conflict("run_not_runnable")
	}
	if r.LeaseUntil != nil && r.LeaseUntil.After(time.Now()) {
		return domain.Conflict("lease_busy")
	}
	if e = checkProviderRetryClaim(ctx, tx, *r); e != nil {
		return e
	}
	var pending bool
	if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM steps WHERE run_id=$1 AND state IN('pending','unknown'))", rid).
		Scan(&pending); e != nil {
		return e
	}
	if pending {
		r.State = domain.Reconciling
		_, e = tx.Exec(
			ctx,
			"UPDATE runs SET state='reconciling',reason='inference_uncertain',lease_owner='',lease_until=NULL WHERE id=$1",
			rid,
		)
		if e != nil {
			return e
		}
		return runEvent(
			ctx,
			tx,
			rid,
			"run.status",
			map[string]any{eventStatusKey: domain.Reconciling, eventReasonKey: "inference_uncertain"},
		)
	}
	_, e = tx.Exec(
		ctx,
		"UPDATE runs SET state='running',lease_owner=$2,lease_until=clock_timestamp()+$3*interval '1 second',fence=fence+1 WHERE id=$1",
		rid,
		owner,
		duration.Seconds(),
	)
	if e != nil {
		return e
	}
	if e = runEvent(ctx, tx, rid, "run.status", map[string]any{eventStatusKey: domain.Running}); e != nil {
		return e
	}
	*r, e = scanRun(tx.QueryRow(ctx, "SELECT "+runColumns+" FROM runs WHERE id=$1", rid))
	return e
}

func (s *Store) completeStep(
	ctx context.Context,
	tx pgx.Tx,
	rid, owner string,
	fence int64,
	sid string,
	result json.RawMessage,
	usage domain.Usage,
) error {
	r, e := leaseRun(ctx, tx, rid, owner, fence)
	if e != nil {
		return e
	}
	var state string
	var reserve int64
	var previous []byte
	e = tx.QueryRow(ctx, "SELECT state,reserve,result FROM steps WHERE id=$1 AND run_id=$2 FOR UPDATE", sid, rid).
		Scan(&state, &reserve, &previous)
	if e != nil {
		return e
	}
	if state == "completed" {
		var storedUsage []byte
		if e = tx.QueryRow(ctx, "SELECT usage FROM steps WHERE id=$1", sid).Scan(&storedUsage); e != nil {
			return e
		}
		var u domain.Usage
		if e = json.Unmarshal(storedUsage, &u); e != nil {
			return e
		}
		var same bool
		if e = tx.QueryRow(ctx, "SELECT result=$2::jsonb FROM steps WHERE id=$1", sid, result).
			Scan(&same); e != nil {
			return e
		}
		if !same || u != usage {
			return domain.Conflict("settlement_conflict")
		}
		return nil
	}
	if state != stepPending && state != "unknown" {
		return domain.Conflict("step_conflict")
	}
	if usage.Microcredits > reserve {
		return domain.Conflict("usage_exceeds_reservation")
	}
	_, e = tx.Exec(
		ctx,
		"UPDATE billing_accounts SET balance=balance-$2,spent=spent+$2,reserved=reserved-$3 WHERE owner_id=$1",
		r.OwnerID,
		usage.Microcredits,
		reserve,
	)
	if e != nil {
		return e
	}
	_, e = tx.Exec(
		ctx,
		"UPDATE runs SET spent_microcredits=spent_microcredits+$2,reserved_microcredits=reserved_microcredits-$3 WHERE id=$1",
		rid,
		usage.Microcredits,
		reserve,
	)
	if e != nil {
		return e
	}
	_, e = tx.Exec(
		ctx,
		"UPDATE steps SET state='completed',result=$2,usage=$3 WHERE id=$1",
		sid,
		result,
		document(usage),
	)
	if e != nil {
		return e
	}
	_, e = tx.Exec(
		ctx,
		"INSERT INTO credit_ledger(id,owner_id,run_id,step_id,settlement_key,amount,kind) VALUES($1,$2,$3,$4,$5,$6,'settle')",
		id(),
		r.OwnerID,
		rid,
		sid,
		"settle:"+sid,
		usage.Microcredits,
	)
	if e != nil {
		return e
	}
	if e = closeProviderRetry(ctx, tx, rid, sid); e != nil {
		return e
	}
	return runEvent(ctx, tx, rid, "usage.updated", usage)
}

func (s *Store) transition(
	ctx context.Context,
	tx pgx.Tx,
	rid, owner string,
	fence int64,
	state domain.RunState,
	reason, result string,
) error {
	r, e := leaseRun(ctx, tx, rid, owner, fence)
	if e != nil {
		return e
	}
	if state != domain.Completed && state != domain.Cancelled && state != domain.Failed && state != domain.Paused &&
		state != domain.WaitingAuth &&
		state != domain.Reconciling {
		return domain.Conflict("invalid_transition")
	}
	if r.CancelRequested {
		state, reason, result = domain.Cancelled, clientCancelReason, ""
	} else if r.PauseRequested && state != domain.Cancelled && state != domain.Failed {
		state, reason, result = domain.Paused, clientPauseReason, ""
	}
	if err := reconcilePausedState(ctx, tx, rid, &state, &reason); err != nil {
		return err
	}
	if state == domain.Completed {
		if err := validateCompletion(ctx, tx, r); err != nil {
			return err
		}
	}
	r.State = state
	r.Reason = reason
	r.Result = result
	_, e = tx.Exec(
		ctx,
		"UPDATE runs SET state=$2,reason=$3,doc=$4,lease_owner='',lease_until=NULL WHERE id=$1",
		rid,
		state,
		reason,
		document(r),
	)
	if e != nil {
		return e
	}
	if state == domain.Completed {
		if _, e = chatEvent(
			ctx,
			tx,
			r.ChatID,
			rid,
			"message.assistant",
			result,
			"agent",
			map[string]string{eventTextKey: result},
		); e != nil {
			return e
		}
		if e = runEvent(ctx, tx, rid, "message.completed", map[string]string{eventTextKey: result}); e != nil {
			return e
		}
	}
	event := "run.status"
	if state == domain.Paused {
		event = "run.resumable"
	}
	return runEvent(
		ctx,
		tx,
		rid,
		event,
		map[string]any{eventStatusKey: state, eventReasonKey: reason, eventRunIDKey: rid},
	)
}

func (s *Store) askQuestion(ctx context.Context, tx pgx.Tx, rid, owner string, fence int64, q domain.Question) error {
	r, e := leaseRun(ctx, tx, rid, owner, fence)
	if e != nil {
		return e
	}
	if r.CancelRequested {
		return cancelInteraction(ctx, tx, r)
	}
	if q.ID == "" {
		q.ID = id()
	}
	if q.ExpiresAt.IsZero() {
		q.ExpiresAt = time.Now().Add(time.Duration(r.SettingsSnapshot.QuestionTimeoutSeconds) * time.Second)
	}
	if r.Interaction.Questions == eventTextKey {
		q.Choices = nil
		q.AllowText = true
	}
	if r.Interaction.Questions == "choices" {
		q.AllowText = false
		if len(q.Choices) == 0 {
			return domain.Conflict("question_unsupported")
		}
	}
	r.Question = &q
	reason := ""
	if r.Interaction.Questions == "none" {
		reason = "client_interaction_disabled"
	}
	state := domain.WaitingInput
	if r.PauseRequested {
		state, reason = domain.Paused, clientPauseReason
	}
	_, e = tx.Exec(
		ctx,
		"UPDATE runs SET state=$2,reason=$3,doc=$4,lease_owner='',lease_until=NULL WHERE id=$1",
		rid,
		state,
		reason,
		document(r),
	)
	if e != nil {
		return e
	}
	if state == domain.Paused {
		if e = runEvent(
			ctx,
			tx,
			rid,
			"run.status",
			map[string]any{eventStatusKey: state, eventReasonKey: reason},
		); e != nil {
			return e
		}
		if e = runEvent(
			ctx,
			tx,
			rid,
			"run.resumable",
			map[string]string{eventRunIDKey: rid, eventReasonKey: reason},
		); e != nil {
			return e
		}
	}
	if _, e = chatEvent(ctx, tx, r.ChatID, rid, "question.created", q.Text, "agent", q); e != nil {
		return e
	}
	return runEvent(ctx, tx, rid, "question.created", q)
}

func (s *Store) control(
	ctx context.Context,
	tx pgx.Tx,
	p domain.Principal,
	rid, key, command string,
	r *domain.Run,
) error {
	uid, e := s.ensureUser(ctx, tx, p)
	if e != nil {
		return e
	}
	*r, e = scanRun(
		tx.QueryRow(
			ctx,
			"SELECT "+runColumns+" FROM runs WHERE id=$1 AND owner_id=$2 AND org_id=$3 AND chat_id IN(SELECT id FROM chats WHERE deleted_at IS NULL) FOR UPDATE",
			rid,
			uid,
			p.OrgID,
		),
	)
	if e != nil {
		return e
	}
	old, e := idem(ctx, tx, scope(p, "control:"+rid), key, command)
	if e != nil {
		return e
	}
	if old != "" {
		return nil
	}
	if r.State.Terminal() {
		return domain.Conflict("run_terminal")
	}
	e = s.applyControl(ctx, tx, r, command)
	if e != nil {
		return e
	}
	if e = saveIdem(ctx, tx, scope(p, "control:"+rid), key, rid, command); e != nil {
		return e
	}
	if e = runEvent(ctx, tx, rid, "run.status", map[string]string{"command": command}); e != nil {
		return e
	}
	*r, e = scanRun(tx.QueryRow(ctx, "SELECT "+runColumns+" FROM runs WHERE id=$1", rid))
	return e
}

func (s *Store) addInput(
	ctx context.Context,
	tx pgx.Tx,
	p domain.Principal,
	rid, key string,
	in domain.RunInput,
	r *domain.Run,
) error {
	uid, e := s.ensureUser(ctx, tx, p)
	if e != nil {
		return e
	}
	*r, e = scanRun(
		tx.QueryRow(
			ctx,
			"SELECT "+runColumns+" FROM runs WHERE id=$1 AND owner_id=$2 AND org_id=$3 AND chat_id IN(SELECT id FROM chats WHERE deleted_at IS NULL) FOR UPDATE",
			rid,
			uid,
			p.OrgID,
		),
	)
	if e != nil {
		return e
	}
	existing, e := idem(ctx, tx, scope(p, "input:"+rid), key, in)
	if e != nil {
		return e
	}
	if existing != "" {
		return nil
	}
	if r.State.Terminal() {
		return domain.Conflict("run_terminal")
	}
	if err := s.applyInput(ctx, tx, r, &in); err != nil {
		return err
	}
	in.ID = id()
	_, e = tx.Exec(ctx, "INSERT INTO run_inputs(id,run_id,doc) VALUES($1,$2,$3)", in.ID, rid, document(in))
	if e != nil {
		return e
	}
	if _, e = chatEvent(ctx, tx, r.ChatID, rid, "input."+in.Kind, in.Text, actor(p), in); e != nil {
		return e
	}
	if in.Kind == "answer" || in.Kind == "approval" {
		_, e = tx.Exec(ctx, "UPDATE runs SET state='queued',reason='',doc=$2 WHERE id=$1", rid, document(r))
		if e != nil {
			return e
		}
		if s.enqueue != nil {
			if e = s.enqueue(ctx, tx, rid); e != nil {
				return e
			}
		}
	}
	if e = saveIdem(ctx, tx, scope(p, "input:"+rid), key, rid, inWithoutID(in)); e != nil {
		return e
	}
	*r, e = scanRun(tx.QueryRow(ctx, "SELECT "+runColumns+" FROM runs WHERE id=$1", rid))
	return e
}

func (s *Store) askApproval(ctx context.Context, tx pgx.Tx, rid, owner string, fence int64, a domain.Approval) error {
	r, e := leaseRun(ctx, tx, rid, owner, fence)
	if e != nil {
		return e
	}
	if r.CancelRequested {
		return cancelInteraction(ctx, tx, r)
	}
	if a.ID == "" || a.OperationID == "" || !a.ExpiresAt.After(time.Now()) {
		return domain.NewError(http.StatusUnprocessableEntity, "invalid_approval")
	}
	r.Approval = &a
	reason := ""
	if !r.Interaction.Approvals {
		reason = "client_interaction_disabled"
	}
	state := domain.WaitingApproval
	if r.PauseRequested {
		state, reason = domain.Paused, clientPauseReason
	}
	_, e = tx.Exec(
		ctx,
		"UPDATE runs SET state=$2,reason=$3,doc=$4,lease_owner='',lease_until=NULL WHERE id=$1",
		rid,
		state,
		reason,
		document(r),
	)
	if e != nil {
		return e
	}
	if state == domain.Paused {
		if e = runEvent(
			ctx,
			tx,
			rid,
			"run.status",
			map[string]any{eventStatusKey: state, eventReasonKey: reason},
		); e != nil {
			return e
		}
		if e = runEvent(
			ctx,
			tx,
			rid,
			"run.resumable",
			map[string]string{eventRunIDKey: rid, eventReasonKey: reason},
		); e != nil {
			return e
		}
	}
	if _, e = chatEvent(ctx, tx, r.ChatID, rid, "approval.created", a.Text, "agent", a); e != nil {
		return e
	}
	return runEvent(ctx, tx, rid, "approval.created", a)
}

func validateCompletion(ctx context.Context, tx pgx.Tx, r domain.Run) error {
	var pending bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM steps WHERE run_id=$1 AND state IN('pending','unknown'))", r.ID).
		Scan(&pending); err != nil {
		return err
	}
	if r.ReservedMicrocredits > 0 || pending {
		return domain.Conflict("unsettled_usage")
	}
	var inputPending bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM run_inputs WHERE run_id=$1 AND NOT applied AND doc->>'kind'='steering')", r.ID).
		Scan(&inputPending); err != nil {
		return err
	}
	if inputPending {
		return domain.Conflict("input_pending")
	}
	return nil
}

func (s *Store) applyControl(ctx context.Context, tx pgx.Tx, r *domain.Run, command string) error {
	rid := r.ID
	var e error
	switch command {
	case "pause":
		if r.State == domain.Running {
			_, e = tx.Exec(ctx, "UPDATE runs SET pause_requested=true WHERE id=$1", rid)
		} else {
			uncertain, err := runUncertainty(ctx, tx, rid)
			if err != nil {
				return err
			}
			next, reason := domain.Paused, clientPauseReason
			if uncertain {
				next, reason = domain.Reconciling, "inference_or_effect_uncertain"
			}
			_, e = tx.Exec(ctx, "UPDATE runs SET state=$2,reason=$3 WHERE id=$1", rid, next, reason)
		}
	case "cancel":
		if r.State == domain.Running {
			_, e = tx.Exec(ctx, "UPDATE runs SET cancel_requested=true WHERE id=$1", rid)
		} else {
			_, e = tx.Exec(ctx, "UPDATE runs SET state='cancelled',reason='client_cancel' WHERE id=$1", rid)
		}
	case "resume":
		return s.resumeRun(ctx, tx, r)
	default:
		return domain.NewError(http.StatusUnprocessableEntity, "invalid_command")
	}
	return e
}

func (s *Store) applyInput(ctx context.Context, tx pgx.Tx, r *domain.Run, in *domain.RunInput) error {
	var e error
	switch in.Kind {
	case "steering":
		if in.Text == "" {
			return domain.NewError(http.StatusUnprocessableEntity, "invalid_input")
		}
	case "answer":
		return applyAnswer(r, in)
	case "approval":
		a := r.Approval
		if r.State != domain.WaitingApproval || a == nil || a.ID != in.ApprovalID || a.Decision != "" {
			return domain.Conflict("input_conflict")
		}
		if !a.ExpiresAt.After(time.Now()) {
			return domain.Conflict("approval_expired")
		}
		if in.Decision != "approve" && in.Decision != "reject" {
			return domain.NewError(http.StatusUnprocessableEntity, "invalid_decision")
		}
		if !r.Interaction.Approvals {
			return domain.Forbidden()
		}
		if s.approval == nil {
			return domain.NewError(http.StatusServiceUnavailable, "approval_handler_unavailable")
		}
		if e = s.approval(ctx, tx, *r, *in); e != nil {
			return e
		}
		a.Decision = in.Decision
	default:
		return domain.NewError(http.StatusUnprocessableEntity, "invalid_input")
	}
	return nil
}

func applyAnswer(r *domain.Run, in *domain.RunInput) error {
	q := r.Question
	if r.State != domain.WaitingInput || q == nil || q.ID != in.QuestionID || q.Answer != nil {
		return domain.Conflict("input_conflict")
	}
	if !q.ExpiresAt.After(time.Now()) {
		return domain.Conflict("question_expired")
	}
	if in.Text != "" && !q.AllowText {
		return domain.NewError(http.StatusUnprocessableEntity, "text_not_allowed")
	}
	if in.Text == "" && len(in.ChoiceIDs) == 0 {
		return domain.NewError(http.StatusUnprocessableEntity, "empty_answer")
	}
	for _, cid := range in.ChoiceIDs {
		valid := false
		for _, c := range q.Choices {
			if c.ID == cid {
				valid = true
			}
		}
		if !valid {
			return domain.NewError(http.StatusUnprocessableEntity, "invalid_choice")
		}
	}
	q.Answer = in
	return nil
}

func reconcilePausedState(ctx context.Context, tx pgx.Tx, rid string, state *domain.RunState, reason *string) error {
	if *state == domain.Paused {
		uncertain, err := runUncertainty(ctx, tx, rid)
		if err != nil {
			return err
		}
		if uncertain {
			*state, *reason = domain.Reconciling, "inference_or_effect_uncertain"
		}
	}
	return nil
}

func (s *Store) resumeRun(ctx context.Context, tx pgx.Tx, r *domain.Run) error {
	rid := r.ID
	var e error
	if r.State != domain.Paused && r.State != domain.Reconciling {
		return domain.Conflict("run_not_paused")
	}
	var pending bool
	if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM steps WHERE run_id=$1 AND state IN('pending','unknown'))", rid).
		Scan(&pending); e != nil {
		return e
	}
	if pending {
		r.State = domain.Reconciling
		return domain.Conflict("inference_uncertain")
	}
	if e = resetProviderRetryForResume(ctx, tx, *r); e != nil {
		return e
	}
	next := domain.Queued
	if r.Question != nil && r.Question.Answer == nil {
		next = domain.WaitingInput
	}
	if r.Approval != nil && r.Approval.Decision == "" {
		next = domain.WaitingApproval
	}
	_, e = tx.Exec(
		ctx,
		"UPDATE runs SET state=$2,reason='',pause_requested=false,cancel_requested=false WHERE id=$1",
		rid,
		next,
	)
	if e == nil && s.enqueue != nil && next == domain.Queued {
		e = s.enqueue(ctx, tx, rid)
	}
	return e
}
