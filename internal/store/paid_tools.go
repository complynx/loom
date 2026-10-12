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
		return nil
	}
	_, err := paidReceiptCost(input, usage)
	return err
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
