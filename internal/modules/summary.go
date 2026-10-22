package modules

import "unicode/utf8"

// DiscoverySummary preserves an authored purpose hint; legacy descriptions remain bounded.
func DiscoverySummary(summary, description string) string {
	if summary != "" {
		return summary
	}
	const legacyLimit = 240
	if len(description) <= legacyLimit {
		return description
	}
	end := legacyLimit
	for !utf8.RuneStart(description[end]) {
		end--
	}
	return description[:end] + "..."
}
