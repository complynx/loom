package runs

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/provider"
)

func TestCompactionPreservesTextAndOpaqueContextWithoutFileBytes(t *testing.T) {
	t.Parallel()
	input := []provider.Item{
		json.RawMessage(
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"Keep original goal"},{"type":"input_image","image_url":"data:image/png;base64,PRIVATE_BYTES"}]}`,
		),
		json.RawMessage(`{"type":"compaction","encrypted_content":"opaque-context"}`),
	}
	out := omitCompactedFileBytes(input)
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "PRIVATE_BYTES")
	require.Contains(t, string(raw), "Keep original goal")
	require.Contains(t, string(raw), "opaque-context")
	require.Contains(t, string(raw), "source service")
	require.Contains(t, string(input[0]), "PRIVATE_BYTES")
}
