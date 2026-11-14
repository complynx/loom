package config

import (
	"errors"
	"net/url"
	"strings"
)

const (
	defaultExternalAgentCallsPerMinute = 30
	maxExternalAgentCallsPerMinute     = 600
	maxExternalAgentScopes             = 32
)

// ExternalAgents configures the MCP endpoint for a user's own agent. It is disabled by default.
type ExternalAgents struct {
	Enabled bool `yaml:"enabled"`
	// Resource is the public URL of the MCP endpoint. Clients use it as the OAuth resource.
	Resource string `yaml:"resource"`
	// Audience is the audience that a token for the MCP endpoint must contain.
	// It must differ from identity.audience: an MCP token must not open the /v1 API.
	Audience string `yaml:"audience"`
	// Scopes are advertised to clients as the scopes to request from the issuer.
	Scopes []string `yaml:"scopes"`
	// CallsPerMinute limits tool calls of one user. Zero uses the default.
	CallsPerMinute int `yaml:"calls_per_minute"`
}

// CallLimit returns the effective per-user tool call limit for one minute.
func (c ExternalAgents) CallLimit() int {
	if c.CallsPerMinute == 0 {
		return defaultExternalAgentCallsPerMinute
	}
	return c.CallsPerMinute
}

// Validate checks the settings. identityAudience is the audience of the /v1 API.
func (c ExternalAgents) Validate(identityAudience string) error {
	if !c.Enabled {
		if c.Resource != "" || c.Audience != "" || len(c.Scopes) > 0 || c.CallsPerMinute != 0 {
			return errors.New("external agent settings require external_agents.enabled")
		}
		return nil
	}
	if err := validateURL(c.Resource); err != nil {
		return errors.New("external_agents.resource must be the public MCP endpoint URL")
	}
	if u, err := url.Parse(c.Resource); err != nil || u.Path == "" || strings.HasSuffix(u.Path, "/") {
		return errors.New("external_agents.resource must contain the endpoint path without a trailing slash")
	}
	if c.Audience == "" || c.Audience == identityAudience {
		return errors.New("external_agents.audience is required and must differ from identity.audience")
	}
	if c.CallsPerMinute < 0 || c.CallsPerMinute > maxExternalAgentCallsPerMinute {
		return errors.New("external_agents.calls_per_minute must be between 1 and 600; zero uses 30")
	}
	if len(c.Scopes) > maxExternalAgentScopes {
		return errors.New("external_agents.scopes has too many entries")
	}
	for _, scope := range c.Scopes {
		if scope == "" || len(scope) > 256 || strings.ContainsFunc(scope, invalidScopeCharacter) {
			return errors.New("external_agents.scopes contains an invalid scope")
		}
	}
	return nil
}

// invalidScopeCharacter rejects what an OAuth scope token and a quoted challenge value cannot contain.
func invalidScopeCharacter(r rune) bool {
	return r <= ' ' || r > '~' || r == '"' || r == '\\'
}
