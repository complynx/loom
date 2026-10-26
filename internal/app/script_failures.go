package app

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/complynx/loom/internal/modules"
	"github.com/complynx/loom/internal/runs"
	"github.com/complynx/loom/internal/sandbox"
)

// safeScriptBroker retains declared validation fields without leaking backend text.
func safeScriptBroker(broker sandbox.Broker) sandbox.Broker {
	return sandbox.BrokerFunc(func(ctx context.Context, m, n string, raw json.RawMessage) (json.RawMessage, error) {
		value, err := broker.Call(ctx, m, n, raw)
		if err == nil {
			return value, nil
		}
		if _, ok := errors.AsType[*runs.Suspension](err); ok {
			return nil, err
		}
		f := *sandbox.SafeFailure(err)
		switch {
		case errors.Is(err, modules.ErrForbidden):
			f.Code = "permission_denied"
			f.Recovery = "Current authority does not allow this method. Do not bypass it."
		case errors.Is(err, modules.ErrUnavailable):
			f.Code = "method_unavailable"
			f.Recovery = "Check the current authorized catalog; the service or method may be unavailable."
		case errors.Is(err, modules.ErrWriteDisabled):
			f.Code = "write_requires_module_call"
			f.Recovery = "Use the module_call tool for external writes; never repeat a write with an unknown outcome."
		}
		if invalid, ok := errors.AsType[*modules.InvalidArgumentsError](err); ok {
			f.Code = invalidArgumentsCode
			f.Fields = invalid.Fields
			f.Recovery = "Read the method contract and correct the declared fields."
		}
		f.Module = m
		f.Method = n
		return nil, &f
	})
}
