package domain

import (
	"encoding/json"
	"time"
)

const (
	// ExternalAgentProfilePermission lets a confirming caller read and change a user's profile.
	ExternalAgentProfilePermission = "external_agent.profile.write"
	// ExternalAgentActionEvent is the passive chat event for one external agent action.
	ExternalAgentActionEvent = "external_agent.action"
	// ExternalAgentActorPrefix starts the source actor of every external agent event.
	ExternalAgentActorPrefix = "external_agent:"
)

// ExternalAgentProfile is the user's consent for an external agent. A confirming
// caller writes it; the external agent cannot change it. Version zero means no
// stored profile: all access is denied.
type ExternalAgentProfile struct {
	Enabled     bool      `json:"enabled"`
	History     bool      `json:"history"`
	Memory      bool      `json:"memory"`
	Skills      bool      `json:"skills"`
	Writes      bool      `json:"writes"`
	Version     int64     `json:"version"`
	ConfirmedBy string    `json:"confirmed_by,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitzero"`
}

// ExternalAgentProfileUpdate replaces the profile. Version must equal the stored version.
type ExternalAgentProfileUpdate struct {
	Enabled bool  `json:"enabled"`
	History bool  `json:"history"`
	Memory  bool  `json:"memory"`
	Skills  bool  `json:"skills,omitempty"`
	Writes  bool  `json:"writes,omitempty"`
	Version int64 `json:"version"`
}

// ExternalApproval is delivered only to the user's confirming chat client.
type ExternalApproval struct {
	ID          string          `json:"operation_id"`
	Subject     string          `json:"subject"`
	OrgID       string          `json:"organization"`
	Client      string          `json:"client"`
	Service     string          `json:"service"`
	Module      string          `json:"module"`
	Method      string          `json:"method"`
	Description string          `json:"description"`
	Arguments   json.RawMessage `json:"arguments,omitempty"`
	Remember    bool            `json:"remember"`
	ExpiresAt   time.Time       `json:"expires_at"`
}

// ExternalCaller is a user who authenticated an external agent with their own token.
// ClientID is the OAuth client that the identity provider reported. It is not verified
// by Loom beyond that report.
type ExternalCaller struct {
	Principal       Principal
	ClientID        string
	Token           string
	BrokerToken     string
	AuthorizationID string
	SessionID       string
}

const MaxExternalAgentAuthorizations = 16

// ExternalAgentAuthorization describes one active OAuth broker login.
type ExternalAgentAuthorization struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ExternalAgentSession is one connection owned by a user and OAuth client.
type ExternalAgentSession struct {
	ID        string    `json:"id"`
	ClientID  string    `json:"client_id"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ExternalTool is one tool that Loom offers to an external agent.
type ExternalTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ExternalActionCall counts calls of one method in an external agent action.
// Unfinished counts calls that were still in flight when the action ended.
type ExternalActionCall struct {
	Module     string `json:"module"`
	Method     string `json:"method"`
	Count      int    `json:"count"`
	Failed     int    `json:"failed,omitempty"`
	Unfinished int    `json:"unfinished,omitempty"`
}

// ExternalAction is the record that Loom writes to chat history for one external agent action.
type ExternalAction struct {
	ID       string               `json:"execution_id"`
	ClientID string               `json:"client_id"`
	Tool     string               `json:"tool"`
	Outcome  string               `json:"outcome"`
	Error    string               `json:"error,omitempty"`
	Calls    []ExternalActionCall `json:"calls"`
}
