package api

import (
	"net/http"

	"github.com/complynx/loom/internal/domain"
)

type recoveryPreparationReceipt struct {
	reconciliationReceipt

	Fence int64 `json:"fence"`
}

func (s *server) prepareRecovery(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	if !s.options.RecoveryMode {
		return domain.Conflict("recovery_mode_required")
	}
	k, err := key(r)
	if err != nil {
		return err
	}
	var body domain.RecoveryPrepareRequest
	if err = s.decode(w, r, &body); err != nil {
		return err
	}
	run, err := s.store.PrepareRecovery(r.Context(), p, r.PathValue("id"), k, body)
	if err != nil {
		return err
	}
	write(w, http.StatusOK, recoveryPreparationReceipt{
		RunID: run.ID, Status: run.State, Reason: run.Reason, UnresolvedSteps: run.UnresolvedSteps,
		SpentMicrocredits: run.SpentMicrocredits, ReservedMicrocredits: run.ReservedMicrocredits,
		Fence: run.Fence})
	return nil
}
