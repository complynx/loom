package domain

const MaxConversationReplacements = 3

// RecoverMessage retries the same logical work. It never grants a new budget.
type RecoverMessage struct {
	Text         string `json:"text"`
	ReplaceRunID string `json:"replace_run_id,omitempty"`
}

type RecoveryReceipt struct {
	Accepted          bool   `json:"accepted"`
	Delivered         bool   `json:"delivered"`
	Abandoned         bool   `json:"abandoned"`
	BlockingMessageID string `json:"blocking_message_id,omitempty"`
	MessageID         string `json:"message_id,omitempty"`
	Sequence          int64  `json:"sequence,omitempty"`
	RunID             string `json:"run_id,omitempty"`
	InputID           string `json:"input_id,omitempty"`
	Status            string `json:"status"`
	Reason            string `json:"reason,omitempty"`
	NextAction        string `json:"next_action"`
}
