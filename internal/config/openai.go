package config

// OpenAICatalogDate dates the reviewed Standard global API rates in micro-USD.
const OpenAICatalogDate = "2026-10-11"

const (
	openAIContextTokens   = 1050000
	openAIMaxOutputTokens = 128000
)

const (
	openAIInputCeiling      = 272000
	openAICompactionTrigger = 244800
	openAICompactionTarget  = 204000
	longInputMultiplier     = 2
	longOutputNumerator     = 3
	longOutputDenominator   = 2
)

// applyOpenAIDefaults seeds only an omitted catalog for the named OpenAI provider.
// Explicit catalogs and persisted database settings remain operator-owned.
func applyOpenAIDefaults(c *Config) {
	if _, ok := c.Providers["openai"]; !ok || len(c.BootstrapSettings.ModelCatalog) != 0 {
		return
	}
	b := &c.BootstrapSettings
	if b.DefaultModelID != "" || len(b.Tariffs) != 0 {
		return
	}
	for _, entry := range []struct {
		id                             string
		input, cached, written, output int64
		none                           bool
	}{
		{"gpt-6-luna", 100000, 10000, 125000, 500000, true},
		{"gpt-6.1-sol", 2000000, 100000, 2500000, 10000000, false},
		{"gpt-6-astra", 10000000, 1000000, 12500000, 50000000, false},
	} {
		efforts := []string{"low", "medium", "high", "xhigh", "max"}
		if entry.none {
			efforts = append([]string{"none"}, efforts...)
		}
		modalities := []string{"text", "image"}
		if c.Providers["openai"].Kind == "codex" || c.Providers["openai"].Kind == "mock" {
			modalities = []string{"text"}
		}
		b.ModelCatalog = append(
			b.ModelCatalog,
			Model{
				InputTokenLimit:           openAIInputCeiling,
				CompactionThresholdTokens: openAICompactionTrigger,
				CompactionTargetTokens:    openAICompactionTarget,
				ID:                        entry.id,
				Provider:                  "openai",
				Model:                     entry.id,
				Efforts:                   efforts,
				MaxEffort:                 "max",
				InputModalities:           modalities,
				ParallelTools:             true,
				Steering:                  true,
				Questions:                 true,
				ContextTokens:             openAIContextTokens,
				MaxOutputTokens:           openAIMaxOutputTokens,
			},
		)
		b.Tariffs = append(
			b.Tariffs,
			Tariff{
				ModelID:                      entry.id,
				InputCreditsPerMillion:       entry.input,
				CachedInputCreditsPerMillion: entry.cached,
				CacheWriteCreditsPerMillion:  entry.written,
				OutputCreditsPerMillion:      entry.output,
				LongContext: &LongContextTariff{
					ThresholdTokens:              openAIInputCeiling,
					InputCreditsPerMillion:       entry.input * longInputMultiplier,
					CachedInputCreditsPerMillion: entry.cached * longInputMultiplier,
					CacheWriteCreditsPerMillion:  entry.written * longInputMultiplier,
					OutputCreditsPerMillion:      entry.output * longOutputNumerator / longOutputDenominator,
				},
			},
		)
	}
	b.DefaultModelID = "gpt-6-luna"
	if b.DefaultEffort == "" {
		b.DefaultEffort = "medium"
	}
}
