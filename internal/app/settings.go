package app

import (
	"errors"
	"github.com/complynx/clx-scripter/internal/config"
	"github.com/complynx/clx-scripter/internal/domain"
)

func bootstrapSettings(c config.Config) (domain.Settings, error) {
	b := c.BootstrapSettings
	s := domain.Settings{ExternalDisclosure: b.ExternalDisclosureDefault, DefaultModelID: b.DefaultModelID, DefaultEffort: b.DefaultEffort, InitialCredits: b.InitialCredits, MaxRunMicrocredits: b.MaxRunMicrocredits, ActiveRunLimit: b.UserActiveRunLimit, HistoryRecentLimit: b.HistoryRecentLimit, MaxSteps: b.MaxSteps, QuestionTimeoutSeconds: b.InputWaitSeconds, RunActiveTimeSeconds: b.RunActiveTimeSeconds, RunLifetimeSeconds: b.RunLifetimeSeconds, InputWaitSeconds: b.InputWaitSeconds, ApprovalWaitSeconds: b.ApprovalWaitSeconds, PausedWaitSeconds: b.PausedWaitSeconds, RegexPatternMaxChars: b.RegexPatternMaxChars, RegexScanMaxBytes: int(b.RegexScanMaxBytes), RegexDeadlineMS: b.RegexDeadlineMS, HistoryRetention: domain.Retention{Mode: b.HistoryRetention.Mode, Days: b.HistoryRetention.Days}, ArtifactRetention: domain.Retention{Mode: b.ArtifactRetention.Mode, Days: b.ArtifactRetention.Days}, TraceRetention: domain.Retention{Mode: b.TraceRetention.Mode, Days: b.TraceRetention.Days}}
	if s.ActiveRunLimit <= 0 {
		s.ActiveRunLimit = c.Execution.ActiveRunsPerUserCeiling
	}
	if s.HistoryRecentLimit <= 0 {
		s.HistoryRecentLimit = 100
	}
	if s.MaxSteps <= 0 {
		s.MaxSteps = 30
	}
	tariffs := map[string]config.Tariff{}
	for _, t := range b.Tariffs {
		tariffs[t.ModelID] = t
	}
	for _, m := range b.ModelCatalog {
		t, ok := tariffs[m.ID]
		if !ok {
			return s, errors.New("each model requires an explicit tariff")
		}
		if !m.ParallelTools || !m.Steering || !m.Questions {
			return s, errors.New("MVP models must support parallel tools, steering and questions")
		}
		maxOutput := m.MaxOutputTokens
		if b.OutputTokenLimit > 0 && maxOutput > b.OutputTokenLimit {
			maxOutput = b.OutputTokenLimit
		}
		images := false
		for _, kind := range m.InputModalities {
			if kind == "image" {
				images = true
			}
		}
		s.Models = append(s.Models, domain.Model{ID: m.ID, Provider: m.Provider, ProviderModel: m.Model, Efforts: m.Efforts, MaxEffort: m.MaxEffort, MaxOutputTokens: maxOutput, InputMicrocreditsPerMillion: t.InputCreditsPerMillion, InputCachedMicrocreditsPerMillion: t.CachedInputCreditsPerMillion, OutputMicrocreditsPerMillion: t.OutputCreditsPerMillion, SupportsImages: images, ContextTokens: int64(m.ContextTokens)})
	}
	return s, nil
}
