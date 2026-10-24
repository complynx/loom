package app

import (
	"context"
	"encoding/json"

	"github.com/complynx/loom/internal/sandbox"
	"github.com/complynx/loom/internal/telemetry"
)

type modelInputReference struct {
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
	Type   string `json:"type,omitempty"`
	Role   string `json:"role,omitempty"`
	ID     string `json:"id,omitempty"`
	CallID string `json:"call_id,omitempty"`
}

func loggedScriptBroker(observer *telemetry.Telemetry, runID string, broker sandbox.Broker) sandbox.Broker {
	if observer == nil || !observer.ActionsEnabled() {
		return broker
	}
	return sandbox.BrokerFunc(
		func(ctx context.Context, module, method string, args json.RawMessage) (json.RawMessage, error) {
			ctx, finish := observer.BeginAction(telemetry.WithRun(ctx, runID), "script_rpc", map[string]any{
				"module": module, "method": method, "arguments": args,
			})
			result, err := broker.Call(ctx, module, method, args)
			finish(result, err)
			return result, err
		},
	)
}
