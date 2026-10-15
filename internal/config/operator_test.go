package config_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
)

func TestOperatorConfig(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		cfg   config.Operator
		valid bool
	}{
		{"proxy", config.Operator{ProxyOrigin: "https://panel.example"}, true},
		{"loopback proxy", config.Operator{ProxyOrigin: "http://127.0.0.1:8080"}, true},
		{"proxy path", config.Operator{ProxyOrigin: "https://panel.example/admin"}, false},
		{"proxy HTTP", config.Operator{ProxyOrigin: "http://panel.example"}, false},
		{"proxy query", config.Operator{ProxyOrigin: "https://panel.example?"}, false},
		{"mixed", config.Operator{ProxyOrigin: "https://panel.example", ClientID: "public", RedirectURI: "https://panel.example/admin"}, false},
		{"disabled", config.Operator{}, true}, {"public PKCE", config.Operator{ClientID: "public", RedirectURI: "https://panel.example/admin"}, true}, {"local QA", config.Operator{ClientID: "public", RedirectURI: "http://localhost:58380/admin"}, true},
		{"partial", config.Operator{ClientID: "public", RedirectURI: ""}, false}, {"external HTTP", config.Operator{ClientID: "public", RedirectURI: "http://panel.example/admin"}, false}, {"wrong callback", config.Operator{ClientID: "public", RedirectURI: "https://panel.example/elsewhere"}, false}, {"query", config.Operator{ClientID: "public", RedirectURI: "https://panel.example/admin?x=1"}, false}, {"encoded path", config.Operator{ClientID: "public", RedirectURI: "https://panel.example/%61dmin"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.cfg.Validate(); (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}

func TestProxyLoginRequiresAnExplicitAdminOrganization(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../../examples/config.yaml")
	require.NoError(t, err)
	cfg, err := config.Parse(data, func(string) (string, bool) { return "configured", true })
	require.NoError(t, err)
	cfg.Operator.ProxyOrigin = "https://panel.example"
	cfg.Identity.AdminOrganizationID = ""
	require.ErrorContains(t, cfg.Validate(), "identity.admin_organization_id")
	cfg.Identity.AdminOrganizationID = "org"
	require.NoError(t, cfg.Validate())
}
