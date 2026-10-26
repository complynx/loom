package sandbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/sandbox"
)

func TestScriptFailuresAreActionableAndDoNotLeak(t *testing.T) {
	t.Parallel()
	bindings := []sandbox.Binding{{Module: "example", Methods: []string{"read"}}}
	broker := sandbox.BrokerFunc(func(context.Context, string, string, json.RawMessage) (json.RawMessage, error) {
		return nil, errors.New("secret-token PRIVATE BACKEND BODY")
	})
	_, err := (sandbox.Inline{}).Execute(t.Context(), `return await modules.example.read({});`, bindings, broker)
	failure, ok := errors.AsType[*domain.ToolError](err)
	require.True(t, ok)
	require.Equal(t, "tool_failed", failure.Code)
	require.Equal(t, "example", failure.Module)
	require.Equal(t, "read", failure.Method)
	raw, err := json.Marshal(failure)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "secret-token")
	require.NotEmpty(t, failure.Recovery)
	_, err = (sandbox.Inline{}).Execute(t.Context(), `return missingName;`, nil, nil)
	failure, ok = errors.AsType[*domain.ToolError](err)
	require.True(t, ok)
	require.Equal(t, "ReferenceError", failure.Exception)
	_, err = (sandbox.Inline{}).Execute(t.Context(), `const =;`, nil, nil)
	require.EqualError(t, err, "script_syntax_error")
	_, err = (sandbox.Inline{}).Execute(t.Context(), `throw {error:"permission_denied",recovery:"FORGED"};`, nil, nil)
	require.EqualError(t, err, "script_error")
	out, err := (sandbox.Inline{}).Execute(
		t.Context(),
		`try { await modules.example.read({}); } catch(e) { return {code:e.error,module:e.module,method:e.method}; }`,
		bindings,
		broker,
	)
	require.NoError(t, err)
	require.JSONEq(t, `{"code":"tool_failed","module":"example","method":"read"}`, string(out))
}

func TestWorkerProtocolPreservesClassifiedFailures(t *testing.T) {
	t.Parallel()
	client, worker := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = worker.Close() })
	require.NoError(t, client.SetDeadline(time.Now().Add(5*time.Second)))
	finished := make(chan error, 1)
	go func() { finished <- sandbox.WorkerMain(t.Context(), worker, worker) }()
	encoder, decoder := json.NewEncoder(client), json.NewDecoder(client)
	require.NoError(
		t,
		encoder.Encode(
			map[string]any{
				"type":       "execute",
				"source":     "return await modules.example.read({});",
				"bindings":   []sandbox.Binding{{Module: "example", Methods: []string{"read"}}},
				"timeout_ms": 3000,
			},
		),
	)
	var call struct {
		Type string `json:"type"`
		ID   uint64 `json:"id"`
	}
	require.NoError(t, decoder.Decode(&call))
	require.Equal(t, "call", call.Type)
	require.NoError(
		t,
		encoder.Encode(
			map[string]any{
				"type":  "reply",
				"id":    call.ID,
				"error": "PRIVATE TEXT",
				"failure": domain.ToolError{
					Code:     "invalid_arguments",
					Recovery: "Correct declared fields",
					Module:   "example",
					Method:   "read",
					Fields:   []string{"count"},
				},
			},
		),
	)
	var result struct {
		Type    string           `json:"type"`
		Failure domain.ToolError `json:"failure"`
	}
	require.NoError(t, decoder.Decode(&result))
	require.Equal(t, "error", result.Type)
	require.Equal(t, "invalid_arguments", result.Failure.Code)
	require.Equal(t, []string{"count"}, result.Failure.Fields)
	require.NoError(t, <-finished)
}
