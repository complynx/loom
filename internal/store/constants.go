package store

import "time"

// Shared event keys and state values keep stored documents consistent.
const (
	eventReasonKey          = "reason"
	eventStatusKey          = "status"
	eventRunIDKey           = "run_id"
	stepPending             = "pending"
	clientCancelReason      = "client_cancel"
	clientPauseReason       = "client_pause"
	eventTextKey            = "text"
	defaultChatAlias        = "default"
	searchBatchSize         = 50
	transactionAttempts     = 3
	queueRecoveryBatchLimit = 500
)

const transactionCleanupTimeout = 500 * time.Millisecond

const (
	searchSemantic           = "semantic"
	searchLiteral            = "literal"
	contextCacheTTL          = 15 * time.Minute
	handleTTL                = 15 * time.Minute
	searchMinOutputBytes     = 1024
	searchMaxOutputBytes     = 256 << 10
	searchMaxSnippetChars    = 4000
	searchInlineDataBytes    = 2048
	searchEnvelopeBytes      = 256
	searchExcerptLeadDivisor = 3
	searchReadPageLimit      = 100
	searchSkippedLimit       = 100
)
