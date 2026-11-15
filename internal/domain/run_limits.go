package domain

const DefaultMaxSteps = 30

// LimitNoteStepKind is the paid step that writes a progress note when a run
// used its step allowance or its own money allowance.
const LimitNoteStepKind = "limit_note"

// Pause reasons of a run that used an allowance or met an empty wallet.
const (
	StepLimitReason        = "step_limit"
	BudgetExhaustedReason  = "budget_exhausted"
	CreditsExhaustedReason = "credits_exhausted"
)

// StepLimitReached uses the same renewal boundary for execution and durable rechecks.
func (r Run) StepLimitReached(completedSteps int) bool {
	limit := r.SettingsSnapshot.MaxSteps
	if limit <= 0 {
		limit = DefaultMaxSteps
	}
	return completedSteps-r.StepBudgetStart >= limit
}
