package api

import (
	"time"

	"github.com/complynx/clx-scripter/internal/domain"
)

type billingAdjustmentRequest struct {
	Amount int64  `json:"amount_microcredits"`
	Reason string `json:"reason"`
}

type reauthorizeSessionRequest struct {
	Version int64 `json:"version"`
}

type authSessionReceipt struct {
	ID        string    `json:"auth_session_id"`
	ExpiresAt time.Time `json:"expires_at"`
	Version   int64     `json:"version"`
}

type modelsResponse struct {
	Models []domain.Model `json:"models"`
}

type historyResponse struct {
	Events []domain.ChatEvent `json:"events"`
}

type reconciliationReceipt struct {
	RunID                string                  `json:"run_id"`
	Status               domain.RunState         `json:"status"`
	Reason               string                  `json:"reason"`
	UnresolvedSteps      []domain.UnresolvedStep `json:"unresolved_steps"`
	SpentMicrocredits    int64                   `json:"spent_microcredits"`
	ReservedMicrocredits int64                   `json:"reserved_microcredits"`
}
