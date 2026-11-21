package app

import (
	"context"
	"encoding/json"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/sandbox"
)

const mcpAccessModule = "mcp_access"

func (t runtimeTools) mcpAccessAvailable(p domain.Principal) bool {
	return t.cfg.ExternalAgents.Enabled && p.Has("chat.read") && !p.ServiceAccount
}

func (t runtimeTools) appendMCPAccessBinding(bindings []sandbox.Binding, p domain.Principal) []sandbox.Binding {
	if !t.mcpAccessAvailable(p) {
		return bindings
	}
	return append(
		bindings,
		sandbox.Binding{Module: mcpAccessModule, Methods: []string{helpListMethod, "revoke", "disable"}},
	)
}

func (t runtimeTools) mcpAccess(
	ctx context.Context,
	p domain.Principal,
	method string,
	raw json.RawMessage,
) (json.RawMessage, error) {
	if !t.mcpAccessAvailable(p) {
		return nil, domain.Forbidden()
	}
	switch method {
	case helpListMethod:
		var in struct{}
		if err := decodeDiagnosticsInput(raw, &in); err != nil {
			return nil, err
		}
		sessions, err := t.repository.ListExternalAgentSessions(ctx, p)
		if err != nil {
			return nil, err
		}
		for i := range sessions {
			sessions[i].ClientID = externalClientID(sessions[i].ClientID)
		}
		return json.Marshal(sessions)
	case "revoke":
		var in struct {
			SessionID string `json:"session_id"`
		}
		if err := decodeDiagnosticsInput(raw, &in); err != nil {
			return nil, err
		}
		if in.SessionID == "" {
			return nil, &domain.ToolError{
				Code:     invalidArgumentsCode,
				Recovery: "Pass a session ID from mcp_access.list.",
			}
		}
		if err := t.repository.RevokeExternalAgentSession(ctx, p, in.SessionID); err != nil {
			return nil, err
		}
		return json.RawMessage(`{"status":"revoked"}`), nil
	case "disable":
		var in struct{}
		if err := decodeDiagnosticsInput(raw, &in); err != nil {
			return nil, err
		}
		if err := t.repository.DisableExternalAgentAccess(ctx, p); err != nil {
			return nil, err
		}
		return json.RawMessage(`{"status":"disabled"}`), nil
	default:
		return nil, domain.NotFound()
	}
}
