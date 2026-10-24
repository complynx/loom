package fileproc

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

//go:embed testdata/shapes.json
var shapeFixture []byte

func TestLottiePreparationRejectsExternalContentAndExpansion(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"p", "u", "x", "fPath"} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			var value map[string]any
			require.NoError(t, json.Unmarshal(shapeFixture, &value))
			value["assets"] = []any{map[string]any{key: "file:///etc/passwd"}}
			data, err := json.Marshal(value)
			require.NoError(t, err)
			input := filepath.Join(t.TempDir(), "input")
			require.NoError(t, os.WriteFile(input, data, 0600))
			_, err = prepareLottie(t.Context(), input, t.TempDir())
			require.ErrorContains(t, err, "external resources and expressions")
		})
	}
	input := filepath.Join(t.TempDir(), "input")
	var compressed bytes.Buffer
	w := gzip.NewWriter(&compressed)
	_, err := w.Write(bytes.Repeat([]byte(" "), int(MaxInputBytes)+1))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	require.NoError(t, os.WriteFile(input, compressed.Bytes(), 0600))
	_, err = prepareLottie(t.Context(), input, t.TempDir())
	require.ErrorContains(t, err, "expanded TGS exceeds input limit")
}

func TestLottiePreparationRejectsDuplicateFields(t *testing.T) {
	t.Parallel()
	data := bytes.Replace(
		shapeFixture,
		[]byte(`"assets":[]`),
		[]byte(`"assets":[{"p":"file:///etc/passwd"}],"assets":[]`),
		1,
	)
	input := filepath.Join(t.TempDir(), "input")
	require.NoError(t, os.WriteFile(input, data, 0600))
	_, err := prepareLottie(t.Context(), input, t.TempDir())
	require.ErrorContains(t, err, "duplicate JSON key")
}
