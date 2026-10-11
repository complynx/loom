package config_test

import (
	"testing"

	"github.com/complynx/loom/internal/config"
)

func TestOperatorConfig(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		cfg   config.Operator
		valid bool
	}{
		{"disabled", config.Operator{}, true}, {"public PKCE", config.Operator{"public", "https://panel.example/admin"}, true}, {"local QA", config.Operator{"public", "http://localhost:58380/admin"}, true},
		{"partial", config.Operator{"public", ""}, false}, {"external HTTP", config.Operator{"public", "http://panel.example/admin"}, false}, {"wrong callback", config.Operator{"public", "https://panel.example/elsewhere"}, false}, {"query", config.Operator{"public", "https://panel.example/admin?x=1"}, false}, {"encoded path", config.Operator{"public", "https://panel.example/%61dmin"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.cfg.Validate(); (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}
