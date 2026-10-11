package store

import (
	"net/http"

	"github.com/complynx/clx-scripter/internal/domain"
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
