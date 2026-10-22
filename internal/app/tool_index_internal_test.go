package app

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/modules"
)

func TestMethodIndexPreservesUnfamiliarCapabilitiesWithBoundedSummaries(t *testing.T) {
	t.Parallel()
	read := modules.Tool{
		Module:      "unfamiliar",
		Method:      "availability",
		Description: "Read availability. " + strings.Repeat("😀 details ", 200),
		InputSchema: json.RawMessage(`{"secret_schema_marker":true}`),
	}
	action := modules.Tool{
		Module:                    "another",
		Method:                    "reserve",
		Effect:                    "write",
		Description:               "Reserve a place.",
		SupportsOperationAdoption: true,
		ConfirmationMode:          "critical",
	}
	index := conciseToolIndex([]modules.Tool{read}, []modules.Tool{action})
	require.Len(t, index, 2)
	require.Equal(t, "another.reserve", index[0].ID)
	require.Equal(t, "write", index[0].Effect)
	require.Equal(t, "unfamiliar.availability", index[1].ID)
	require.Equal(t, "read", index[1].Effect)
	require.True(t, utf8.ValidString(index[1].Summary))
	require.LessOrEqual(t, len(index[1].Summary), 243)
	raw, err := json.Marshal(index)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "secret_schema_marker")
	require.Equal(t, "critical", action.ConfirmationMode, "presentation must not mutate the execution contract")
}
