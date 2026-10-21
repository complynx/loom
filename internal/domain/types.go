package domain

import (
	"encoding/json"
	"net/http"
	"time"
)

// DefaultArtifactTTL is the lifetime of temporary file bytes.
const DefaultArtifactTTL = 24 * time.Hour

type Principal struct {
	Issuer, Subject, Actor, OrgID string
	Permissions                   map[string]bool
	ExpiresAt                     time.Time
}

func (p Principal) Has(s string) bool { return p.Permissions[s] }

type Error struct {
	Code      string
	Status    int
	Retryable bool
	Message   string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Code
}
func NewError(s int, c string) error { return &Error{Code: c, Status: s} }
func Conflict(c string) error        { return NewError(http.StatusConflict, c) }

var ErrNotFound = &Error{Code: "not_found", Status: http.StatusNotFound}

func NotFound() error  { return ErrNotFound }
func Forbidden() error { return NewError(http.StatusForbidden, "forbidden") }

type RunState string

const (
	Queued          RunState = "queued"
	Running         RunState = "running"
	WaitingInput    RunState = "waiting_input"
	WaitingApproval RunState = "waiting_approval"
	WaitingAuth     RunState = "waiting_auth"
	Reconciling     RunState = "reconciling"
	Paused          RunState = "paused"
	Completed       RunState = "completed"
	Cancelled       RunState = "cancelled"
	Failed          RunState = "failed"
)

func (s RunState) Terminal() bool { return s == Completed || s == Cancelled || s == Failed }

type Chat struct {
	ID        string    `json:"id"`
	OwnerID   string    `json:"owner_id"`
	OrgID     string    `json:"org_id"`
	IsDefault bool      `json:"is_default"`
	CreatedAt time.Time `json:"created_at"`
}

const (
	ReplyModeAgent     = "agent"
	ReplyModeProtocol  = "protocol"
	ReplyModeExactText = "exact_text"
	InputSteering      = "steering"
	InputApproval      = "approval"
	DecisionApprove    = "approve"
	DecisionReject     = "reject"
)

type Interaction struct {
	ReplyMode string `json:"reply_mode,omitempty" enum:"protocol,agent"`
	Questions string `json:"questions"`
	Approvals bool   `json:"approvals"`
}
type Input struct {
	Text        string   `json:"text"`
	ArtifactIDs []string `json:"artifact_ids,omitempty"`
}
type Budget struct {
	MaxMicrocredits int64 `json:"max_microcredits"`
}
type Delivery struct {
	Mode   string `json:"mode"`
	WaitMS int    `json:"wait_ms"`
}
type CreateRun struct {
	PreviousRunID    string      `json:"previous_run_id,omitempty"`
	AutoAuthSession  bool        `json:"-"`
	ChatID           string      `json:"chat_id,omitempty"`
	AuthSessionID    string      `json:"auth_session_id,omitempty"`
	Input            Input       `json:"input"`
	Interaction      Interaction `json:"interaction"`
	DisconnectPolicy string      `json:"disconnect_policy,omitempty"`
	Delivery         Delivery    `json:"delivery"`
	Budget           Budget      `json:"budget"`
	BillingAccountID string      `json:"billing_account_id,omitempty"`
}
type Run struct {
	ConversationRevision int64            `json:"conversation_revision,omitempty"`
	ConversationInput    *RunInput        `json:"conversation_input,omitempty"`
	PreviousRunID        string           `json:"previous_run_id,omitempty"`
	ActiveMilliseconds   int64            `json:"active_milliseconds"`
	UnresolvedSteps      []UnresolvedStep `json:"unresolved_steps,omitempty"`
	DisconnectPolicy     string           `json:"disconnect_policy"`
	Model                Model            `json:"model_snapshot"`
	SettingsSnapshot     Settings         `json:"settings_snapshot"`
	ID                   string           `json:"run_id"`
	ChatID               string           `json:"chat_id"`
	OwnerID              string           `json:"owner_id"`
	ActorID              string           `json:"actor_id"`
	OrgID                string           `json:"org_id"`
	State                RunState         `json:"status"`
	Reason               string           `json:"reason,omitempty"`
	Input                Input            `json:"input"`
	Result               string           `json:"result,omitempty"`
	ModelID              string           `json:"model_id"`
	Effort               string           `json:"effort"`
	Interaction          Interaction      `json:"interaction"`
	AuthSessionID        string           `json:"auth_session_id,omitempty"`
	MaxMicrocredits      int64            `json:"max_microcredits"`
	SpentMicrocredits    int64            `json:"spent_microcredits"`
	ReservedMicrocredits int64            `json:"reserved_microcredits"`
	Version              int64            `json:"version"`
	Fence                int64            `json:"-"`
	LeaseOwner           string           `json:"-"`
	LeaseUntil           *time.Time       `json:"-"`
	PauseRequested       bool             `json:"pause_requested"`
	CancelRequested      bool             `json:"cancel_requested"`
	CreatedAt            time.Time        `json:"created_at"`
	UpdatedAt            time.Time        `json:"updated_at"`
	Question             *Question        `json:"question,omitempty"`
	Approval             *Approval        `json:"approval,omitempty"`
}
type Event struct {
	Seq       int64           `json:"seq"`
	Type      string          `json:"type"`
	Data      json.RawMessage `json:"data"`
	CreatedAt time.Time       `json:"created_at"`
}
type ChatEvent struct {
	Action        string          `json:"action,omitempty"`
	Seq           int64           `json:"seq"`
	Type          string          `json:"type"`
	Text          string          `json:"text"`
	Data          json.RawMessage `json:"data,omitempty"`
	RunID         string          `json:"run_id,omitempty"`
	SourceActor   string          `json:"source_actor"`
	SourceEventID string          `json:"source_event_id,omitempty"`
	OccurredAt    time.Time       `json:"occurred_at"`
	ReceivedAt    time.Time       `json:"received_at"`
}
type AppendEvent struct {
	SourceEventID string          `json:"source_event_id"`
	Type          string          `json:"type"`
	Action        string          `json:"action,omitempty"`
	Text          string          `json:"text"`
	Data          json.RawMessage `json:"data,omitempty"`
	OccurredAt    time.Time       `json:"occurred_at"`
}
type SearchRequest struct {
	CaseSensitive   bool      `json:"case_sensitive,omitempty"`
	MaxBytes        int       `json:"max_bytes,omitempty"`
	SnippetChars    int       `json:"snippet_chars,omitempty"`
	Embedding       []float32 `json:"embedding,omitempty"`
	EmbeddingModel  string    `json:"embedding_model,omitempty"`
	Offset          int       `json:"offset,omitempty"`
	Mode            string    `json:"mode"`
	Query           string    `json:"query"`
	AfterSeq        int64     `json:"after_seq,omitempty"`
	BeforeSeq       int64     `json:"before_seq,omitempty"`
	SourceServiceID string    `json:"source_service_id,omitempty"`
	EventTypes      []string  `json:"event_types,omitempty"`
	Limit           int       `json:"limit"`
}
type SearchResult struct {
	NextTextOffset int         `json:"next_text_offset,omitempty"`
	TextPartial    bool        `json:"text_partial,omitempty"`
	Reason         string      `json:"reason,omitempty"`
	SkippedSeqs    []int64     `json:"skipped_seqs,omitempty"`
	TruncatedSeqs  []int64     `json:"truncated_seqs,omitempty"`
	NextOffset     int         `json:"next_offset,omitempty"`
	Events         []ChatEvent `json:"events"`
	Partial        bool        `json:"partial"`
	NextCursor     int64       `json:"next_cursor,omitempty"`
}
type Choice struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}
type Question struct {
	ID        string    `json:"question_id"`
	Text      string    `json:"text"`
	Choices   []Choice  `json:"choices,omitempty"`
	AllowText bool      `json:"allow_text"`
	ExpiresAt time.Time `json:"expires_at"`
	Answer    *RunInput `json:"answer,omitempty"`
}
type Approval struct {
	FailureCode      string          `json:"failure_code,omitempty"`
	ConfirmationText string          `json:"confirmation_text,omitempty"`
	ReplyMode        string          `json:"reply_mode"`
	ConsentRevision  int64           `json:"consent_revision,omitempty"`
	Call             *ToolCall       `json:"call,omitempty"`
	ResolutionCallID string          `json:"resolution_call_id,omitempty"`
	DecisionInputID  string          `json:"decision_input_id,omitempty"`
	ID               string          `json:"approval_id"`
	Text             string          `json:"text"`
	OperationID      string          `json:"operation_id"`
	Method           string          `json:"method"`
	Arguments        json.RawMessage `json:"arguments"`
	ArgumentHash     string          `json:"argument_hash"`
	ConfirmationID   string          `json:"confirmation_id,omitempty"`
	Precondition     string          `json:"precondition,omitempty"`
	Decision         string          `json:"decision,omitempty"`
	ExpiresAt        time.Time       `json:"expires_at"`
}
type RunInput struct {
	Presentation *InputPresentation `json:"presentation,omitempty"`
	Context      *InputContext      `json:"context,omitempty"`
	ID           string             `json:"id,omitempty"`
	Kind         string             `json:"kind"`
	Text         string             `json:"text,omitempty"`
	QuestionID   string             `json:"question_id,omitempty"`
	ChoiceIDs    []string           `json:"choice_ids,omitempty"`
	ApprovalID   string             `json:"approval_id,omitempty"`
	Decision     string             `json:"decision,omitempty"`
	Applied      bool               `json:"applied"`
}

// InputPresentation carries client observations, not authorization or commands.
type InputPresentation struct {
	ReplyTo    string `json:"reply_to,omitempty"`
	QuestionID string `json:"question_id,omitempty"`
	ApprovalID string `json:"approval_id,omitempty"`
}

// InputContext is stamped by Loom while holding the run lock.
type InputContext struct {
	Revision    int64  `json:"revision"`
	SourceActor string `json:"source_actor"`
	QuestionID  string `json:"question_id,omitempty"`
	ApprovalID  string `json:"approval_id,omitempty"`
}

type InteractionResolution struct {
	InputID      string `json:"input_id"`
	ApprovalID   string `json:"approval_id"`
	ArgumentHash string `json:"argument_hash"`
	Decision     string `json:"decision"`
}

// LongContextTariff replaces all token rates above the input threshold.
type LongContextTariff struct {
	ThresholdTokens                   int64 `json:"threshold_tokens"`
	InputMicrocreditsPerMillion       int64 `json:"input_microcredits_per_million"`
	CachedInputMicrocreditsPerMillion int64 `json:"cached_input_microcredits_per_million"`
	CacheWriteMicrocreditsPerMillion  int64 `json:"cache_write_microcredits_per_million"`
	OutputMicrocreditsPerMillion      int64 `json:"output_microcredits_per_million"`
}
type Model struct {
	InputTokenLimit                       int64              `json:"input_token_limit,omitempty"`
	CompactionThresholdTokens             int64              `json:"compaction_threshold_tokens,omitempty"`
	CompactionTargetTokens                int64              `json:"compaction_target_tokens,omitempty"`
	InputCacheWriteMicrocreditsPerMillion int64              `json:"input_cache_write_microcredits_per_million,omitempty"`
	LongContext                           *LongContextTariff `json:"long_context,omitempty"`
	Provider                              string             `json:"provider,omitempty"`
	InputCachedMicrocreditsPerMillion     int64              `json:"input_cached_microcredits_per_million"`
	ContextTokens                         int64              `json:"context_tokens"`
	ID                                    string             `json:"id"`
	ProviderModel                         string             `json:"provider_model"`
	Efforts                               []string           `json:"efforts"`
	MaxEffort                             string             `json:"max_effort"`
	MaxOutputTokens                       int                `json:"max_output_tokens"`
	InputMicrocreditsPerMillion           int64              `json:"input_microcredits_per_million"`
	OutputMicrocreditsPerMillion          int64              `json:"output_microcredits_per_million"`
	SupportsImages                        bool               `json:"supports_images"`
}
type Preferences struct {
	ModelID string `json:"model_id"`
	Effort  string `json:"effort"`
	Version int64  `json:"version"`
}
type UserPolicy struct {
	Models         []Model `json:"models"`
	ActiveRunLimit int     `json:"active_run_limit"`
	Version        int64   `json:"version"`
}
type Settings struct {
	TotalActiveRunLimit     int       `json:"total_active_run_limit"`
	ScriptTimeoutMS         int       `json:"script_timeout_ms"`
	ParallelRPCLimit        int       `json:"parallel_rpc_limit"`
	ContextInputTokenBudget int       `json:"context_input_token_budget"`
	RunActiveTimeSeconds    int       `json:"run_active_time_seconds"`
	RegexPatternMaxChars    int       `json:"regex_pattern_max_chars"`
	RegexScanMaxBytes       int       `json:"regex_scan_max_bytes"`
	RegexDeadlineMS         int       `json:"regex_deadline_ms"`
	RunLifetimeSeconds      int       `json:"run_lifetime_seconds"`
	InputWaitSeconds        int       `json:"input_wait_seconds"`
	ApprovalWaitSeconds     int       `json:"approval_wait_seconds"`
	PausedWaitSeconds       int       `json:"paused_wait_seconds"`
	HistoryRetention        Retention `json:"history_retention"`
	ArtifactRetention       Retention `json:"artifact_retention"`
	TraceRetention          Retention `json:"trace_retention"`
	ExternalDisclosure      bool      `json:"external_disclosure"`
	Models                  []Model   `json:"models"`
	DefaultModelID          string    `json:"default_model_id"`
	DefaultEffort           string    `json:"default_effort"`
	InitialCredits          int64     `json:"initial_credits"`
	MaxRunMicrocredits      int64     `json:"max_run_microcredits"`
	ActiveRunLimit          int       `json:"active_run_limit"`
	HistoryRecentLimit      int       `json:"history_recent_limit"`
	MaxSteps                int       `json:"max_steps"`
	QuestionTimeoutSeconds  int       `json:"question_timeout_seconds"`
	Version                 int64     `json:"version"`
}
type Balance struct {
	AvailableMicrocredits int64 `json:"available_microcredits"`
	SpentMicrocredits     int64 `json:"spent_microcredits"`
	ReservedMicrocredits  int64 `json:"reserved_microcredits"`
}
type Message struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}
type Usage struct {
	AudioInputTokens  int64  `json:"audio_input_tokens,omitempty"`
	TextInputTokens   int64  `json:"text_input_tokens,omitempty"`
	AudioDurationMS   int64  `json:"audio_duration_ms,omitempty"`
	CacheWriteTokens  int64  `json:"cache_write_tokens,omitempty"`
	CachedInputTokens int64  `json:"cached_input_tokens"`
	InputTokens       int64  `json:"input_tokens"`
	OutputTokens      int64  `json:"output_tokens"`
	Microcredits      int64  `json:"microcredits"`
	RequestID         string `json:"request_id,omitempty"`
}
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}
type Turn struct {
	Text              string     `json:"text,omitempty"`
	ToolCalls         []ToolCall `json:"tool_calls,omitempty"`
	Question          *Question  `json:"question,omitempty"`
	Usage             Usage      `json:"usage"`
	ProviderRequestID string     `json:"provider_request_id,omitempty"`
}
type Step struct {
	ID        string          `json:"id"`
	RunID     string          `json:"run_id"`
	Ordinal   int64           `json:"ordinal"`
	Kind      string          `json:"kind"`
	State     string          `json:"state"`
	Input     json.RawMessage `json:"input"`
	Result    json.RawMessage `json:"result,omitempty"`
	Usage     Usage           `json:"usage"`
	CreatedAt time.Time       `json:"created_at"`
}
type Operation struct {
	ID           string          `json:"operation_id"`
	RunID        string          `json:"run_id"`
	StepID       string          `json:"step_id"`
	Method       string          `json:"method"`
	Arguments    json.RawMessage `json:"arguments"`
	State        string          `json:"state"`
	Result       json.RawMessage `json:"result,omitempty"`
	ErrorCode    string          `json:"error_code,omitempty"`
	ArgumentHash string          `json:"argument_hash"`
	CreatedAt    time.Time       `json:"created_at"`
}
type Artifact struct {
	ExternalDisclosureAllowed bool      `json:"external_disclosure_allowed"`
	SourceServiceID           string    `json:"source_service_id,omitempty"`
	SourceExternalID          string    `json:"source_external_id,omitempty"`
	SourceModule              string    `json:"source_module,omitempty"`
	SourceMethod              string    `json:"source_method,omitempty"`
	SourceFetchMethod         string    `json:"source_fetch_method,omitempty"`
	SourceReferenceID         string    `json:"reference_id,omitempty"`
	ExpiresAt                 time.Time `json:"expires_at"`
	ID                        string    `json:"artifact_id"`
	OwnerID                   string    `json:"owner_id"`
	OrgID                     string    `json:"org_id"`
	MIME                      string    `json:"mime"`
	SHA256                    string    `json:"sha256"`
	Size                      int64     `json:"size"`
	CreatedAt                 time.Time `json:"created_at"`
}
type AuthSession struct {
	ID, Issuer, Subject, Actor, OrgID string
	Envelope                          json.RawMessage `json:"-"`
	ExpiresAt                         time.Time
	Version                           int64
	RevokedAt                         *time.Time
}

type ContextSummary struct {
	ChatID     string `json:"chat_id"`
	ThroughSeq int64  `json:"through_seq"`
	Text       string `json:"text"`
	SourceHash string `json:"source_hash"`
	ModelID    string `json:"model_id"`
}

type ReconcileEvidence struct {
	Kind      string `json:"kind"`
	Reference string `json:"reference"`
	Reason    string `json:"reason"`
}

// RecoveryPrepareRequest records why an expired execution is quarantined.
type RecoveryPrepareRequest struct {
	ExpectedFence int64             `json:"expected_fence" minimum:"1"`
	Evidence      ReconcileEvidence `json:"evidence"`
}

type ReconcileStepRequest struct {
	Usage    Usage             `json:"usage"`
	Evidence ReconcileEvidence `json:"evidence"`
	Result   json.RawMessage   `json:"result,omitempty"`
}
type UnresolvedStep struct {
	ID                   string `json:"step_id"`
	Kind                 string `json:"kind"`
	ReservedMicrocredits int64  `json:"reserved_microcredits"`
	ClientRequestID      string `json:"client_request_id"`
}

type Retention struct {
	Mode string `json:"mode"`
	Days int    `json:"days"`
}

// CachedContext contains conversation data only, never tool execution state or authorization.
type CachedContext struct {
	Items      []json.RawMessage `json:"items"`
	ThroughSeq int64             `json:"through_seq"`
}

type EventEmbedding struct {
	Seq    int64     `json:"seq"`
	Model  string    `json:"model"`
	Vector []float32 `json:"vector"`
}

type ReadHistoryRequest struct {
	EventSeq     int64 `json:"event_seq,omitempty"`
	OffsetChars  int   `json:"offset_chars,omitempty"`
	AfterSeq     int64 `json:"after_seq,omitempty"`
	BeforeSeq    int64 `json:"before_seq,omitempty"`
	Limit        int   `json:"limit,omitempty"`
	MaxBytes     int   `json:"max_bytes,omitempty"`
	SnippetChars int   `json:"snippet_chars,omitempty"`
}

type ArtifactHandle struct {
	ID          string    `json:"handle_id"`
	RunID       string    `json:"run_id"`
	ArtifactID  string    `json:"artifact_id"`
	ExpiresAt   time.Time `json:"expires_at"`
	ContentPath string    `json:"content_path"`
}
