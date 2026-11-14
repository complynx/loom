package config

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

const (
	defaultExternalAgentCallsPerMinute = 30
	maxExternalAgentCallsPerMinute     = 600
	maxExternalAgentScopes             = 32
	maxExternalTokenServices           = 32
	minExternalTokenSecret             = 16
	maxExternalTokenSecret             = 512
)

var externalTokenServiceID = regexp.MustCompile("^[a-z][a-z0-9_.-]{0,127}$")

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
	// TokenServices approves the services that issue service tokens for external
	// agents. The key is a service ID; the value is the secret that authenticates
	// Loom to that service. Loom sends the agent's token only to these services.
	// Without an entry only help, history and memory are available.
	TokenServices map[string]string `yaml:"token_services" json:"-"`
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
			len(c.TokenServices) > 0 {
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
	if len(c.TokenServices) > maxExternalTokenServices {
		return errors.New("external_agents.token_services has too many entries")
	}
	for service, secret := range c.TokenServices {
		if !externalTokenServiceID.MatchString(service) || len(secret) < minExternalTokenSecret ||
			len(secret) > maxExternalTokenSecret ||
			strings.ContainsFunc(secret, func(r rune) bool { return r <= ' ' || r > '~' }) {
			return errors.New(
				"external_agents.token_services maps a service ID to a secret of 16..512 printable characters",
			)
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
