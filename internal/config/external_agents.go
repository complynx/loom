package config

import (
	"crypto/rsa"
	"errors"
	"fmt"
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
	// ClientID, ClientSecret, ClientKeyBase64 and ClientKeyFile are optional
	// introspection credentials for MCP tokens. They have the meaning of the same
	// identity fields. Without them Loom uses the identity credentials. An issuer
	// that introspects a token only for a client named in the token's audience
	// needs them: an MCP token must not contain identity.audience.
	ClientID        string `yaml:"client_id"`
	ClientSecret    string `yaml:"client_secret"     json:"-"`
	ClientKeyBase64 string `yaml:"client_key_base64" json:"-"`
	ClientKeyFile   string `yaml:"client_key_file"`
	// ClientKeyID and ClientPrivateKey are set by ResolveCredentials, not in YAML.
	ClientKeyID      string          `yaml:"-" json:"-"`
	ClientPrivateKey *rsa.PrivateKey `yaml:"-" json:"-"`
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

// ResolveCredentials loads the optional application key of the MCP endpoint
// with the rules of Identity.ResolveCredentials.
func (c ExternalAgents) ResolveCredentials() (ExternalAgents, error) {
	resolved, err := Identity{
		ClientID: c.ClientID, ClientSecret: c.ClientSecret,
		ClientKeyBase64: c.ClientKeyBase64, ClientKeyFile: c.ClientKeyFile,
		ClientKeyID: c.ClientKeyID, ClientPrivateKey: c.ClientPrivateKey,
	}.ResolveCredentials()
	if err != nil {
		return ExternalAgents{}, fmt.Errorf("external_agents: %w", err)
	}
	c.ClientID, c.ClientKeyBase64 = resolved.ClientID, resolved.ClientKeyBase64
	c.ClientKeyID, c.ClientPrivateKey = resolved.ClientKeyID, resolved.ClientPrivateKey
	return c, nil
}

// OwnCredentials reports that MCP tokens are introspected with separate credentials.
func (c ExternalAgents) OwnCredentials() bool { return c.ClientID != "" }

// Validate checks the settings. identityAudience is the audience of the /v1 API.
func (c ExternalAgents) Validate(identityAudience string) error {
	credential := c.ClientSecret != "" || c.ClientPrivateKey != nil
	if !c.Enabled {
		if c.Resource != "" || c.Audience != "" || len(c.Scopes) > 0 || c.CallsPerMinute != 0 ||
			len(c.TokenServices) > 0 || c.ClientID != "" || c.ClientKeyBase64 != "" || c.ClientKeyFile != "" ||
			credential {
			return errors.New("external agent settings require external_agents.enabled")
		}
		return nil
	}
	if err := c.validateTokenServices(); err != nil {
		return err
	}
	if c.OwnCredentials() != credential {
		return errors.New("external_agents introspection requires client_id with client_secret, or an application key")
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
	return nil
}

func (c ExternalAgents) validateTokenServices() error {
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
