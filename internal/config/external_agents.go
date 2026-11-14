package config

import (
	"errors"
	"net/url"
	"slices"
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
	// ServiceToken lets external agents call marked service methods. Loom exchanges
	// the agent's token for a separate service token. Without it only help,
	// history and memory are available.
	ServiceToken ExternalServiceToken `yaml:"service_token"`
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
		if c.Resource != "" || c.Audience != "" || len(c.Scopes) > 0 || c.CallsPerMinute != 0 ||
			!c.ServiceToken.empty() {
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
	if !validScopes(c.Scopes) {
		return errors.New("external_agents.scopes contains an invalid scope")
	}
	return c.ServiceToken.validate(identityAudience, c.Audience)
}

// ExternalServiceToken configures the OAuth token exchange that gives Loom a
// separate token for downstream services. The external agent never receives it.
type ExternalServiceToken struct {
	// ClientID and ClientSecret authenticate Loom at the issuer's token endpoint.
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
	// Audiences are the audiences of the downstream services.
	Audiences []string `yaml:"audiences"`
	// Scopes are requested for the service token.
	Scopes []string `yaml:"scopes"`
}

// Enabled reports that external agents can call service methods.
func (c ExternalServiceToken) Enabled() bool { return c.ClientID != "" }

func (c ExternalServiceToken) empty() bool {
	return c.ClientID == "" && c.ClientSecret == "" && len(c.Audiences) == 0 && len(c.Scopes) == 0
}

// validate refuses a service token that would also open one of Loom's endpoints.
func (c ExternalServiceToken) validate(loomAudiences ...string) error {
	if c.empty() {
		return nil
	}
	if c.ClientID == "" || c.ClientSecret == "" || len(c.Audiences) == 0 {
		return errors.New("external_agents.service_token requires client_id, client_secret and audiences")
	}
	if len(c.Audiences) > maxExternalAgentScopes || len(c.Scopes) > maxExternalAgentScopes || !validScopes(c.Scopes) {
		return errors.New("external_agents.service_token has invalid audiences or scopes")
	}
	for _, audience := range c.Audiences {
		if audience == "" || len(audience) > 256 || slices.Contains(loomAudiences, audience) {
			return errors.New("external_agents.service_token.audiences must name downstream services, not Loom")
		}
	}
	return nil
}

func validScopes(scopes []string) bool {
	for _, scope := range scopes {
		if scope == "" || len(scope) > 256 || strings.ContainsFunc(scope, invalidScopeCharacter) {
			return false
		}
	}
	return true
}

// invalidScopeCharacter rejects what an OAuth scope token and a quoted challenge value cannot contain.
func invalidScopeCharacter(r rune) bool {
	return r <= ' ' || r > '~' || r == '"' || r == '\\'
}
