package config

import (
	"os"
	"strings"
	"testing"
)

func TestLoadDeploymentConfiguration(t *testing.T) {
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
	cfg, e := Parse(b, lookup)
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
			if _, e := Parse(tc.body, tc.lookup); e == nil {
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
	cfg.Observability.LogPayloads = false
	injected, e := Parse(b, func(k string) (string, bool) {
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
	cfg.BootstrapSettings.TraceRetention = Retention{Mode: "duration", Days: 30}
	if cfg.Validate() == nil {
		t.Fatal("unsupported trace expiry accepted")
	}
	cfg.BootstrapSettings.TraceRetention = Retention{Mode: "forever"}
	unsupported := strings.Replace(string(b), "  lease_seconds:", "  recovery_retry_window_seconds: 86400"+string(rune(10))+"  lease_seconds:", 1)
	if _, e := Parse([]byte(unsupported), lookup); e == nil {
		t.Fatal("unsupported recovery window accepted")
	}
	cfg.Identity.RefreshableExchangeEnabled = true
	if cfg.Validate() == nil {
		t.Fatal("ungated refresh exchange accepted")
	}
}
func TestEveryModelNeedsExplicitTariff(t *testing.T) {
	b, e := os.ReadFile("../../examples/config.yaml")
	if e != nil {
		t.Fatal(e)
	}
	cfg, e := Parse(b, func(k string) (string, bool) {
		if strings.Contains(k, "ENDPOINT") {
			return "https://models.example", true
		}
		return "configured", true
	})
	if e != nil {
		t.Fatal(e)
	}
	cfg.BootstrapSettings.ModelCatalog = []Model{{ID: "m", Provider: "primary", Model: "model", ContextTokens: 1000, MaxOutputTokens: 100}}
	cfg.BootstrapSettings.DefaultModelID = "m"
	if cfg.Validate() == nil {
		t.Fatal("missing tariff accepted")
	}
	cfg.BootstrapSettings.Tariffs = []Tariff{{ModelID: "m"}}
	if e := cfg.Validate(); e != nil {
		t.Fatal(e)
	}
}
