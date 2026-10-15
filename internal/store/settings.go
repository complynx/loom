package store

import (
	"net/http"

	"github.com/complynx/loom/internal/domain"
)

func normalizeSettings(v domain.Settings) domain.Settings {
	if v.RegexPatternMaxChars == 0 {
		v.RegexPatternMaxChars = 256
	}
	if v.RegexScanMaxBytes == 0 {
		v.RegexScanMaxBytes = 1048576
	}
	if v.RegexDeadlineMS == 0 {
		v.RegexDeadlineMS = 500
	}
	if v.RunActiveTimeSeconds == 0 {
		v.RunActiveTimeSeconds = 600
	}
	return v
}
func validateLifecycleSettings(v domain.Settings) error {
	if v.TraceRetention.Mode == "duration" {
		return domain.NewError(http.StatusUnprocessableEntity, "unsupported_trace_retention")
	}
	for _, seconds := range []int{v.RunActiveTimeSeconds, v.RunLifetimeSeconds, v.InputWaitSeconds, v.ApprovalWaitSeconds, v.PausedWaitSeconds, v.QuestionTimeoutSeconds} {
		if seconds < 0 || seconds > 2147483647 {
			return domain.NewError(http.StatusUnprocessableEntity, "invalid_deadline")
		}
	}
	for _, retention := range []domain.Retention{v.HistoryRetention, v.ArtifactRetention, v.TraceRetention} {
		switch retention.Mode {
		case "", "forever":
			if retention.Days != 0 {
				return domain.NewError(http.StatusUnprocessableEntity, "invalid_retention")
			}
		case "duration":
			if retention.Days <= 0 || retention.Days > 365000 {
				return domain.NewError(http.StatusUnprocessableEntity, "invalid_retention")
			}
		default:
			return domain.NewError(http.StatusUnprocessableEntity, "invalid_retention")
		}
	}
	return nil
}

// SetExecutionCeilings binds editable limits to immutable deployment ceilings before serving.
func (s *Store) SetExecutionCeilings(script, parallel, contextBudget int, defaults domain.Settings) {
	s.scriptCeiling, s.parallelCeiling, s.contextCeiling = script, parallel, contextBudget
	s.executionDefaults = defaults
}

func (s *Store) normalizeExecutionSettings(v domain.Settings) domain.Settings {
	if v.TotalActiveRunLimit == 0 {
		v.TotalActiveRunLimit = s.executionDefaults.TotalActiveRunLimit
	}
	if v.ScriptTimeoutMS == 0 {
		v.ScriptTimeoutMS = s.executionDefaults.ScriptTimeoutMS
	}
	if v.ParallelRPCLimit == 0 {
		v.ParallelRPCLimit = s.executionDefaults.ParallelRPCLimit
	}
	if v.ContextInputTokenBudget == 0 {
		v.ContextInputTokenBudget = s.executionDefaults.ContextInputTokenBudget
	}
	return v
}

func (s *Store) validateExecutionSettings(v domain.Settings) error {
	for _, pair := range [][2]int{{v.TotalActiveRunLimit, s.totalCeiling}, {v.ScriptTimeoutMS, s.scriptCeiling}, {v.ParallelRPCLimit, s.parallelCeiling}, {v.ContextInputTokenBudget, s.contextCeiling}} {
		if pair[0] < 0 || (pair[1] > 0 && (pair[0] < 1 || pair[0] > pair[1])) {
			return domain.NewError(http.StatusUnprocessableEntity, "hard_execution_ceiling")
		}
	}
	return nil
}
