package domain

import "time"

// DiagnosticsOverview exposes operational aggregates without execution payloads.
type DiagnosticsOverview struct {
	GeneratedAt                  time.Time        `json:"generated_at"`
	Runs                         map[string]int64 `json:"runs"`
	Queue                        map[string]int64 `json:"queue,omitempty"`
	ReservedMicrocredits         int64            `json:"reserved_microcredits"`
	UnknownInferenceSteps        int64            `json:"unknown_inference_steps"`
	UnresolvedWrites             int64            `json:"unresolved_writes"`
	PlatformAttempts             int64            `json:"platform_attempts"`
	PlatformExposureMicrocredits int64            `json:"platform_exposure_microcredits"`
	OldestQueuedSeconds          float64          `json:"oldest_queued_seconds"`
	OldestReservationSeconds     float64          `json:"oldest_reservation_seconds"`
	ExpiredLeases                int64            `json:"expired_leases"`
}

type DiagnosticRun struct {
	ID                   string     `json:"id"`
	ChatID               string     `json:"chat_id"`
	State                RunState   `json:"state"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
	LeaseUntil           *time.Time `json:"lease_until,omitempty"`
	PauseRequested       bool       `json:"pause_requested"`
	CancelRequested      bool       `json:"cancel_requested"`
	SpentMicrocredits    int64      `json:"spent_microcredits"`
	ReservedMicrocredits int64      `json:"reserved_microcredits"`
	PlatformAttempts     int64      `json:"platform_attempts"`
}

type DiagnosticsRunsRequest struct {
	State   string `json:"state"`
	AfterID string `json:"after_id"`
	Limit   int    `json:"limit"`
}

type DiagnosticsRunsPage struct {
	Items      []DiagnosticRun `json:"items"`
	NextCursor string          `json:"next_cursor,omitempty"`
	Partial    bool            `json:"partial"`
}

type DiagnosticStep struct {
	ID                  string    `json:"id"`
	Kind                string    `json:"kind"`
	State               string    `json:"state"`
	ReserveMicrocredits int64     `json:"reserve_microcredits"`
	CreatedAt           time.Time `json:"created_at"`
	Usage               Usage     `json:"usage"`
}

type DiagnosticWrite struct {
	ID        string    `json:"id"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type DiagnosticAttempt struct {
	StepID                       string    `json:"step_id"`
	Status                       string    `json:"status"`
	ReservedExposureMicrocredits int64     `json:"reserved_exposure_microcredits"`
	CreatedAt                    time.Time `json:"created_at"`
}

type DiagnosticsRunDetail struct {
	ProviderRetry   *ProviderRetry      `json:"provider_retry,omitempty"`
	Run             DiagnosticRun       `json:"run"`
	Steps           []DiagnosticStep    `json:"steps"`
	Writes          []DiagnosticWrite   `json:"writes"`
	Attempts        []DiagnosticAttempt `json:"attempts"`
	StepsPartial    bool                `json:"steps_partial"`
	WritesPartial   bool                `json:"writes_partial"`
	AttemptsPartial bool                `json:"attempts_partial"`
}
