package domain

const DefaultMaxSteps = 30

// StepLimitReached uses the same renewal boundary for execution and durable rechecks.
func (r Run) StepLimitReached(completedSteps int) bool {
	limit := r.SettingsSnapshot.MaxSteps
	if limit <= 0 {
		limit = DefaultMaxSteps
	}
	return completedSteps-r.StepBudgetStart >= limit
}
