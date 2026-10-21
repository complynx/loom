// Package operations persists write intent before invoking a remote module.
package operations

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrApprovalRequired    = errors.New("operation requires approval")
	ErrBinding             = errors.New("operation binding changed")
	ErrExpired             = errors.New("operation approval expired")
	ErrLease               = errors.New("run lease no longer permits dispatch")
	ErrState               = errors.New("operation state does not permit this action")
	ErrAdoptionUnsupported = errors.New("operation adoption is not supported")
)

// Binding identifies the exact authority and module contract behind an intent.
type Binding struct {
	UserID       string `json:"user_id"`
	Issuer       string `json:"issuer"`
	Subject      string `json:"subject"`
	Actor        string `json:"actor"`
	OrgID        string `json:"org_id"`
	ContractHash string `json:"contract_hash"`
}

type Request struct {
	BackendOperationID string          `json:"backend_operation_id,omitempty"`
	RunID              string          `json:"run_id"`
	CallID             string          `json:"call_id"`
	Module             string          `json:"module"`
	Method             string          `json:"method"`
	Arguments          json.RawMessage `json:"arguments"`
	Binding            Binding         `json:"binding"`
	Mode               string          `json:"mode"`
}

// BackendID preserves the original service identity when adopting a preparation.
func (op Operation) BackendID() string {
	if op.BackendOperationID != "" {
		return op.BackendOperationID
	}
	return op.ID
}

type Inspection struct {
	Preparation Preparation
	Result      Result
}

// Inspector reads an existing operation under current authority. It must never prepare or execute.
type Inspector interface {
	Inspect(context.Context, Operation) (Inspection, error)
}

type Preparation struct {
	Arguments     json.RawMessage `json:"normalized_arguments,omitempty"`
	Description   string          `json:"description,omitempty"`
	Token         string          `json:"confirmation_id,omitempty"`
	Preconditions json.RawMessage `json:"preconditions,omitempty"`
	ExpiresAt     time.Time       `json:"expires_at"`
}

type Operation struct {
	Request

	ConsentRevision int64 `json:"consent_revision,omitempty"`

	ID           string          `json:"operation_id"`
	State        string          `json:"state"`
	FailureCode  string          `json:"failure_code,omitempty"`
	CancelReason string          `json:"cancel_reason,omitempty"`
	ArgumentHash string          `json:"argument_hash"`
	BindingHash  string          `json:"binding_hash"`
	Preparation  Preparation     `json:"preparation"`
	Result       json.RawMessage `json:"result,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
}

type Result struct {
	State  string          `json:"state"`
	Output json.RawMessage `json:"output,omitempty"`
}

// Backend must treat Prepare as side-effect free. Execute must send ID as its
// stable idempotency key; Status must never execute the requested action.
type Backend interface {
	Prepare(context.Context, Operation) (Preparation, error)
	Execute(context.Context, Operation) (Result, error)
	Status(context.Context, Operation) (Result, error)
}

type Lease struct {
	Owner string
	Fence int64
}
type Authorize func(context.Context, Operation) error

// NotDispatchedError proves that backend execution stopped before its RPC.
type NotDispatchedError struct{ Cause error }

func (e *NotDispatchedError) Error() string { return "operation was not dispatched" }
func (e *NotDispatchedError) Unwrap() error { return e.Cause }

// Approval binds approval to the rendered operation, including preconditions.
type Approval struct {
	ConsentRevision int64
	Binding         Binding
	BindingHash     string
	Approve         bool
}
