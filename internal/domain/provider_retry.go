package domain

import "time"

// ProviderRetry stores only retry timing and sanitized rejection metadata.
type ProviderRetry struct {
	Attempts      int       `json:"attempts"`
	NextAttemptAt time.Time `json:"next_attempt_at"`
	Deadline      time.Time `json:"deadline"`
	Code          string    `json:"code"`
}

// ProviderRetryWaitError prevents a claim before the persisted provider schedule.
type ProviderRetryWaitError struct{ Until time.Time }

func (*ProviderRetryWaitError) Error() string { return "provider retry not due" }
