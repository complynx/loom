package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/dop251/goja"

	"github.com/complynx/loom/internal/domain"
)

func failure(code, recovery string) *domain.ToolError {
	return &domain.ToolError{Code: code, Recovery: recovery}
}

// SafeFailure is also used at IPC boundaries; arbitrary error text stays private.
func SafeFailure(err error) *domain.ToolError {
	if known, ok := errors.AsType[*domain.ToolError](err); ok {
		return known
	}
	if known, ok := errors.AsType[*domain.Error](err); ok {
		switch known.Code {
		case "skill_version_conflict", "memory_version_conflict":
			return failure(known.Code, "Read the current version and resolve the conflict before editing again.")
		}
		switch known.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			return failure("permission_denied", "Current authority does not allow this call. Do not bypass it.")
		case http.StatusNotFound:
			return failure("not_found", "Check the identifier and current authorized help catalog.")
		case http.StatusUnprocessableEntity, http.StatusBadRequest:
			return failure("invalid_arguments", "Read the method contract and correct the arguments.")
		case http.StatusConflict:
			return failure("state_conflict", "Read current state before deciding whether to continue.")
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return failure(
			"script_timeout",
			"Reduce work per script. Inspect completed effects before repeating any writes.",
		)
	}
	if errors.Is(err, context.Canceled) {
		return failure("script_cancelled", "Inspect current run state and completed effects before continuing.")
	}
	return failure(
		"tool_failed",
		"Inspect the selected contract and current state. Do not repeat writes with an unknown outcome.",
	)
}

func javascriptFailure(_ *goja.Runtime, value goja.Value) *domain.ToolError {
	// Preserve a broker rejection by identity, never by trusting script-authored keys.
	if object, ok := value.(*goja.Object); ok {
		name := object.Get("name")
		if name != nil {
			switch name.String() {
			case "TypeError", "ReferenceError", "SyntaxError", "RangeError":
				out := failure(
					"script_error",
					"Correct the JavaScript and check method names and result shapes in help.",
				)
				out.Exception = name.String()
				return out
			}
		}
	}
	return failure(
		"script_error",
		"Correct the JavaScript. Inspect help for the methods used; do not repeat completed writes.",
	)
}

func safeBroker(broker Broker) Broker {
	return BrokerFunc(func(ctx context.Context, m, n string, a json.RawMessage) (json.RawMessage, error) {
		value, err := broker.Call(ctx, m, n, a)
		if err != nil {
			f := *SafeFailure(err)
			f.Module = m
			f.Method = n
			return nil, &f
		}
		return value, nil
	})
}
