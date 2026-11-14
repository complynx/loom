package config_test

import (
	"testing"

	"github.com/complynx/loom/internal/config"
)

func TestExternalAgentsConfig(t *testing.T) {
	t.Parallel()
	enabled := func(change func(*config.ExternalAgents)) config.ExternalAgents {
		cfg := config.ExternalAgents{Enabled: true, Resource: "https://loom.example.com/mcp"}
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
		{"audience", enabled(func(c *config.ExternalAgents) { c.Audience = "mcp-project" }), true},
		{"origin", enabled(func(c *config.ExternalAgents) { c.AllowedOrigins = []string{"https://app.example.com"} }), true},
		{"origin path", enabled(func(c *config.ExternalAgents) { c.AllowedOrigins = []string{"https://app.example.com/"} }), false},
		{"origin wildcard", enabled(func(c *config.ExternalAgents) { c.AllowedOrigins = []string{"*"} }), false},
		{"limit", enabled(func(c *config.ExternalAgents) { c.CallsPerMinute = 600 }), true},
		{"limit too high", enabled(func(c *config.ExternalAgents) { c.CallsPerMinute = 601 }), false},
		{"negative limit", enabled(func(c *config.ExternalAgents) { c.CallsPerMinute = -1 }), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.cfg.Validate(); (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
	if limit := (config.ExternalAgents{Enabled: true}).CallLimit(); limit != 30 {
		t.Fatalf("default call limit = %d", limit)
	}
}
