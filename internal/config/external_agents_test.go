package config_test

import (
	"testing"

	"github.com/complynx/loom/internal/config"
)

func TestExternalAgentsConfig(t *testing.T) {
	t.Parallel()
	enabled := func(change func(*config.ExternalAgents)) config.ExternalAgents {
		cfg := config.ExternalAgents{Enabled: true, Resource: "https://loom.example.com/mcp", Audience: "mcp"}
		change(&cfg)
		return cfg
	}
	for _, tc := range []struct {
		name  string
		cfg   config.ExternalAgents
		valid bool
	}{
		{"disabled", config.ExternalAgents{}, true},
		{"disabled with settings", config.ExternalAgents{Resource: "https://loom.example.com/mcp"}, false},
		{"enabled", enabled(func(*config.ExternalAgents) {}), true},
		{"loopback", enabled(func(c *config.ExternalAgents) { c.Resource = "http://localhost:8080/mcp" }), true},
		{"missing resource", enabled(func(c *config.ExternalAgents) { c.Resource = "" }), false},
		{"external HTTP", enabled(func(c *config.ExternalAgents) { c.Resource = "http://loom.example.com/mcp" }), false},
		{"origin only", enabled(func(c *config.ExternalAgents) { c.Resource = "https://loom.example.com" }), false},
		{"trailing slash", enabled(func(c *config.ExternalAgents) { c.Resource = "https://loom.example.com/mcp/" }), false},
		{"query", enabled(func(c *config.ExternalAgents) { c.Resource = "https://loom.example.com/mcp?x=1" }), false},
		{"scopes", enabled(func(c *config.ExternalAgents) { c.Scopes = []string{"openid", "urn:example:aud"} }), true},
		{"scope with space", enabled(func(c *config.ExternalAgents) { c.Scopes = []string{"open id"} }), false},
		{"scope with quote", enabled(func(c *config.ExternalAgents) { c.Scopes = []string{`open"id`} }), false},
		{"missing audience", enabled(func(c *config.ExternalAgents) { c.Audience = "" }), false},
		{"token service", enabled(func(c *config.ExternalAgents) { c.TokenServices = tokenService("chat-client") }), true},
		{"token service with a short secret", enabled(func(c *config.ExternalAgents) {
			c.TokenServices = map[string]string{"chat-client": "short"}
		}), false},
		{"token service with a space in the secret", enabled(func(c *config.ExternalAgents) {
			c.TokenServices = map[string]string{"chat-client": "sixteen characters"}
		}), false},
		{"token service with an invalid ID", enabled(func(c *config.ExternalAgents) {
			c.TokenServices = tokenService("Chat Client")
		}), false},
		{"token service while disabled", config.ExternalAgents{TokenServices: tokenService("chat-client")}, false},
		{"shared audience", enabled(func(c *config.ExternalAgents) { c.Audience = "loom" }), false},
		{"limit", enabled(func(c *config.ExternalAgents) { c.CallsPerMinute = 600 }), true},
		{"limit too high", enabled(func(c *config.ExternalAgents) { c.CallsPerMinute = 601 }), false},
		{"negative limit", enabled(func(c *config.ExternalAgents) { c.CallsPerMinute = -1 }), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.cfg.Validate("loom"); (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
	if limit := (config.ExternalAgents{Enabled: true}).CallLimit(); limit != 30 {
		t.Fatalf("default call limit = %d", limit)
	}
}

func tokenService(id string) map[string]string {
	return map[string]string{id: "sixteen-characters"}
}
