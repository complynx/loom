package operations

import (
	"bytes"
	"encoding/json"
)

// ExternalArgumentCipher protects the durable copy of a private write input.
type ExternalArgumentCipher interface {
	SealOperationArguments(operationID string, arguments json.RawMessage) (json.RawMessage, error)
	OpenOperationArguments(operationID string, envelope json.RawMessage) (json.RawMessage, error)
}

func (c *Coordinator) protectExternalArguments(op *Operation) error {
	if c.ArgumentCipher == nil || op.ExternalSessionID == "" || op.Mode != noConfirmation ||
		op.RememberApproval || op.BackendOperationID != "" {
		return ErrBinding
	}
	sealed, err := c.ArgumentCipher.SealOperationArguments(op.ID, op.Arguments)
	if err != nil {
		return err
	}
	op.ProtectedArguments = sealed
	op.Arguments = nil
	op.ArgumentHash = ""
	return nil
}

func (c *Coordinator) externalArguments(op Operation) (json.RawMessage, error) {
	if len(op.ProtectedArguments) == 0 {
		return op.Arguments, nil
	}
	if c.ArgumentCipher == nil {
		return nil, ErrBinding
	}
	return c.ArgumentCipher.OpenOperationArguments(op.ID, op.ProtectedArguments)
}

func (c *Coordinator) matchesExternalArguments(op Operation, arguments json.RawMessage) bool {
	if len(op.ProtectedArguments) == 0 {
		return op.ArgumentHash == digest(arguments)
	}
	saved, err := c.externalArguments(op)
	return err == nil && bytes.Equal(saved, arguments)
}

// Only pass the hydrated copy to the backend. State transitions persist the
// original operation with sealed arguments.
func (op Operation) withArguments(args json.RawMessage) Operation {
	op.Arguments = args
	return op
}
