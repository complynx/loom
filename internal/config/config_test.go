package config_test

import (
	"os"
	"strings"
	"testing"

	"github.com/complynx/clx-scripter/internal/config"
)

func TestLoadDeploymentConfiguration(t *testing.T) {
	t.Parallel()
	b, e := os.ReadFile("../../examples/config.yaml")
	if e != nil {
		t.Fatal(e)
	}
	lookup := func(k string) (string, bool) {
		if k == "CLX_MODEL_ENDPOINT" {
			return "https://models.example/v1", true
		}
		return "configured", true
	}
	cfg, e := config.Parse(b, lookup)
	if e != nil {
		t.Fatal(e)
	}
	if cfg.Database.DSN != "configured" || cfg.Execution.ShutdownDeadlineMS != 5000 {
		t.Fatalf("unexpected config %+v", cfg.Execution)
	}
	for _, tc := range []struct {
		name   string
		body   []byte
		lookup func(string) (string, bool)
	}{{"missing env", b, func(string) (string, bool) { return "", false }}, {"unknown field", append(append([]byte{}, b...), []byte("\nunknown_setting: true\n")...), lookup}, {"multiple documents", append(append([]byte{}, b...), []byte("\n---\nhttp: {}\n")...), lookup}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, parseErr := config.Parse(tc.body, tc.lookup); parseErr == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	cfg.Sandbox.EngineCandidate = "unknown"
	if cfg.Validate() == nil {
		t.Fatal("unsupported sandbox engine accepted")
	}
	cfg.Sandbox.EngineCandidate = "goja"
	cfg.Observability.LogPayloads = true
	if cfg.Validate() == nil {
		t.Fatal("unsupported raw payload logging accepted")
	}
	cfg.Observability.LogLevel = "debug"
	if validationErr := cfg.Validate(); validationErr != nil {
		t.Fatal(validationErr)
	}
	cfg.Observability.LogPayloads = false
	injected, e := config.Parse(b, func(k string) (string, bool) {
		if k == "CLX_DATABASE_DSN" {
			return "operator-value" + string(rune(10)) + "http:" + string(rune(10)) + "  listen: malicious", true
		}
		return lookup(k)
	})
	if e != nil {
		t.Fatal(e)
	}
	if injected.HTTP.Listen != ":8080" {
		t.Fatal("environment value changed YAML structure")
	}
	cfg.BootstrapSettings.TraceRetention = config.Retention{Mode: "duration", Days: 30}
	if cfg.Validate() == nil {
		t.Fatal("unsupported trace expiry accepted")
	}
	cfg.BootstrapSettings.TraceRetention = config.Retention{Mode: "forever"}
	unsupported := strings.Replace(
		string(b),
		"  lease_seconds:",
		"  recovery_retry_window_seconds: 86400"+string(rune(10))+"  lease_seconds:",
		1,
	)
	if _, parseErr := config.Parse([]byte(unsupported), lookup); parseErr == nil {
		t.Fatal("unsupported recovery window accepted")
	}
	cfg.Identity.RefreshableExchangeEnabled = true
	if cfg.Validate() == nil {
		t.Fatal("ungated refresh exchange accepted")
	}
}
func TestEveryModelNeedsExplicitTariff(t *testing.T) {
	t.Parallel()
	b, e := os.ReadFile("../../examples/config.yaml")
	if e != nil {
		t.Fatal(e)
	}
	cfg, e := config.Parse(b, func(k string) (string, bool) {
		if strings.Contains(k, "ENDPOINT") {
			return "https://models.example", true
		}
		return "configured", true
	})
	if e != nil {
		t.Fatal(e)
	}
	cfg.BootstrapSettings.ModelCatalog = []config.Model{
		{ID: "m", Provider: "openai", Model: "model", ContextTokens: 1000, MaxOutputTokens: 100},
	}
	cfg.BootstrapSettings.DefaultModelID = "m"
	cfg.BootstrapSettings.DefaultEffort = ""
	if cfg.Validate() == nil {
		t.Fatal("missing tariff accepted")
	}
	cfg.BootstrapSettings.Tariffs = []config.Tariff{{ModelID: "m"}}
	if validationErr := cfg.Validate(); validationErr != nil {
		t.Fatal(validationErr)
	}
}

func TestPreviousKeyConfiguration(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("../../examples/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(b, func(k string) (string, bool) {
		if strings.Contains(k, "ENDPOINT") {
			return "https://models.example", true
		}
		return "configured", true
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Identity.PreviousEncryptionKeys = map[string]string{"old": "/run/secrets/old"}
	if validationErr := cfg.Validate(); validationErr != nil {
		t.Fatal(validationErr)
	}
	for _, keys := range []map[string]string{{"": "path"}, {"old": ""}, {cfg.Identity.EncryptionKeyID: "path"}} {
		cfg.Identity.PreviousEncryptionKeys = keys
		if cfg.Validate() == nil {
			t.Fatal("invalid previous key configuration accepted")
		}
	}
}
