package store

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/complynx/loom/internal/domain"
)

const transcriptionStepKind = "transcription"

// reconciliationReceiptReserve admits paid tools only for manual receipt settlement.
func reconciliationReceiptReserve(ctx context.Context, tx pgx.Tx, rid, sid string) (int64, error) {
	var kind, state string
	var reserve int64
	if err := tx.QueryRow(ctx, "SELECT kind,state,reserve FROM steps WHERE id=$1 AND run_id=$2 FOR UPDATE", sid, rid).
		Scan(&kind, &state, &reserve); err != nil {
		return 0, err
	}
	if kind != transcriptionStepKind {
		return reconciliationReserve(ctx, tx, rid, sid)
	}
	if state != platformAttemptUnknown && state != stepPending {
		return 0, domain.Conflict("step_not_reconcilable")
	}
	return reserve, nil
}

// validateStepSettlement checks the reserve and frozen tariff before settlement.
func validateStepSettlement(ctx context.Context, tx pgx.Tx, rid, sid string, reserve int64, usage domain.Usage) error {
	if usage.Microcredits > reserve {
		return domain.Conflict("usage_exceeds_reservation")
	}
	var kind string
	var input []byte
	if err := tx.QueryRow(ctx, "SELECT kind,input FROM steps WHERE id=$1 AND run_id=$2", sid, rid).
		Scan(&kind, &input); err != nil {
		return err
	}
	if kind != transcriptionStepKind {
		if kind == domain.TranscriptReuseStepKind && usage != (domain.Usage{}) {
			return domain.Conflict("invalid_transcript_reuse_usage")
		}
		return nil
	}
	_, err := paidReceiptCost(input, usage)
	return err
}

func validStepReservation(kind string, reserve int64) bool {
	return reserve >= 0 && (kind != domain.TranscriptReuseStepKind || reserve == 0)
}

func reserveStepLedger(ctx context.Context, tx pgx.Tx, r domain.Run, step domain.Step, reserve int64) error {
	if err := bindTranscriptStep(ctx, tx, r, step); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, "UPDATE runs SET reserved_microcredits=reserved_microcredits+$2 WHERE id=$1", r.ID, reserve)
	if err != nil {
		return err
	}
	if step.Kind == domain.TranscriptReuseStepKind {
		return nil
	}
	_, err = tx.Exec(
		ctx,
		"INSERT INTO credit_ledger(id,owner_id,run_id,step_id,settlement_key,amount,kind) VALUES($1,$2,$3,$4,$5,$6,'reserve')",
		id(),
		r.OwnerID,
		r.ID,
		step.ID,
		"reserve:"+step.ID,
		reserve,
	)
	return err
}

func settleStepLedger(
	ctx context.Context,
	tx pgx.Tx,
	r domain.Run,
	sid string,
	usage domain.Usage,
	result json.RawMessage,
) error {
	var kind string
	if err := tx.QueryRow(ctx, "SELECT kind FROM steps WHERE id=$1", sid).Scan(&kind); err != nil {
		return err
	}
	if kind == domain.TranscriptReuseStepKind {
		return nil
	}
	if kind == transcriptionStepKind && usage == (domain.Usage{}) && rejectedTranscriptResult(result) {
		if _, err := tx.Exec(ctx, "DELETE FROM audio_transcripts WHERE id=$1 AND result IS NULL", sid); err != nil {
			return err
		}
	}
	_, err := tx.Exec(
		ctx,
		"INSERT INTO credit_ledger(id,owner_id,run_id,step_id,settlement_key,amount,kind) VALUES($1,$2,$3,$4,$5,$6,'settle')",
		id(),
		r.OwnerID,
		r.ID,
		sid,
		"settle:"+sid,
		usage.Microcredits,
	)
	return err
}

// Only a definite, zero-usage rejection releases an unused transcript identity.
func rejectedTranscriptResult(result json.RawMessage) bool {
	var envelope struct {
		Output string `json:"output"`
	}
	var output struct {
		Error string `json:"error"`
	}
	return json.Unmarshal(result, &envelope) == nil && json.Unmarshal([]byte(envelope.Output), &output) == nil &&
		output.Error == "transcription_rejected"
}

func paidReceiptCost(input []byte, usage domain.Usage) (int64, error) {
	var intent domain.PaidToolIntent
	if err := json.Unmarshal(input, &intent); err != nil {
		return 0, err
	}
	cost, err := domain.PaidToolCost(usage, intent.Billing)
	if err != nil {
		return 0, err
	}
	if cost != usage.Microcredits {
		return 0, domain.NewError(http.StatusUnprocessableEntity, "receipt_tariff_mismatch")
	}
	return cost, nil
}

func reconciliationCost(
	ctx context.Context,
	tx pgx.Tx,
	rid, sid string,
	usage domain.Usage,
	model domain.Model,
	reserve int64,
) (int64, error) {
	var kind string
	var input []byte
	if err := tx.QueryRow(ctx, "SELECT kind,input FROM steps WHERE id=$1 AND run_id=$2", sid, rid).
		Scan(&kind, &input); err != nil {
		return 0, err
	}
	if kind != transcriptionStepKind {
		return validatedReceiptCost(usage, model, reserve)
	}
	cost, err := paidReceiptCost(input, usage)
	if err != nil {
		return 0, err
	}
	if cost > reserve {
		return 0, domain.Conflict("usage_exceeds_reservation")
	}
	return cost, nil
}

// reconciliationOutput preserves the tool protocol for manually settled audio calls.
func reconciliationOutput(
	ctx context.Context,
	tx pgx.Tx,
	rid, sid string,
	result json.RawMessage,
) (json.RawMessage, error) {
	var kind string
	var input []byte
	if err := tx.QueryRow(ctx, "SELECT kind,input FROM steps WHERE id=$1 AND run_id=$2", sid, rid).
		Scan(&kind, &input); err != nil {
		return nil, err
	}
	if kind != transcriptionStepKind {
		return result, nil
	}
	var intent domain.PaidToolIntent
	if err := json.Unmarshal(input, &intent); err != nil {
		return nil, err
	}
	if intent.Call.ID == "" || !json.Valid(result) {
		return nil, domain.Conflict("invalid_paid_tool_result")
	}
	return json.Marshal(struct {
		Type   string `json:"type"`
		CallID string `json:"call_id"`
		Output string `json:"output"`
	}{"function_call_output", intent.Call.ID, string(result)})
}
