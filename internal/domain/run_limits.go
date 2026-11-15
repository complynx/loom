package domain

const DefaultMaxSteps = 30

// LimitNoteStepKind is the paid step that writes a progress note at the step limit.
const LimitNoteStepKind = "limit_note"

// StepLimitReached uses the same renewal boundary for execution and durable rechecks.
func (r Run) StepLimitReached(completedSteps int) bool {
	limit := r.SettingsSnapshot.MaxSteps
	if limit <= 0 {
		limit = DefaultMaxSteps
	}
	return completedSteps-r.StepBudgetStart >= limit
}
