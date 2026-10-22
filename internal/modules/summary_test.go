package modules_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/modules"
)

func TestSummaryValidationAtServiceModuleAndMethodLevels(t *testing.T) {
	t.Parallel()
	for _, level := range []string{"service", "module", "method"} {
		t.Run(level, func(t *testing.T) {
			t.Parallel()
			for _, summary := range []string{"Choose an appointment.", strings.Repeat("界", 160), strings.Repeat("x", 161), "   "} {
				manifest := example(t)
				manifest.Modules = manifest.Modules[:1]
				switch level {
				case "service":
					manifest.Summary = summary
				case "module":
					manifest.Modules[0].Summary = summary
				case "method":
					manifest.Modules[0].Methods[0].Summary = summary
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_ = json.NewEncoder(w).Encode(manifest)
				}))
				registry, err := modules.NewRegistry(
					modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
						return []modules.Endpoint{endpoint(t, server.URL, "one")}, nil
					}),
					modules.Options{},
				)
				require.NoError(t, err)
				err = registry.Refresh(t.Context())
				server.Close()
				if summary == strings.Repeat("x", 161) || summary == "   " {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
			}
		})
	}
}

func TestLegacySummaryFallbackPreservesUTF8(t *testing.T) {
	t.Parallel()
	description := strings.Repeat("界", 100)
	summary := modules.DiscoverySummary("", description)
	require.True(t, utf8.ValidString(summary))
	require.LessOrEqual(t, len(summary), 243)
	require.True(t, strings.HasSuffix(summary, "..."))
	purpose := strings.Repeat("界", 160)
	require.Equal(t, purpose, modules.DiscoverySummary(purpose, description))
}
