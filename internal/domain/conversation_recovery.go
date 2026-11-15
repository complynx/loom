package domain

// MaxConversationReplacements bounds successive replacements of open runs.
// A terminal run ends the chain.
const MaxConversationReplacements = 3

// RecoverMessage is one user message for a chat. With ReplaceRunID it replaces
// an open run and keeps the remaining allowance of that run.
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
