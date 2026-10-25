package app

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/artifacts"
	"github.com/complynx/loom/internal/domain"
)

func TestModelReceivesOriginalThenLabeledAlpha(t *testing.T) {
	t.Parallel()
	picture := artifacts.ModelArtifact{
		Metadata: domain.Artifact{MIME: "image/webp"},
		Bytes:    []byte("original"),
		AlphaPNG: []byte("alpha"),
	}
	content := modelImageContent(picture)
	require.Len(t, content, 3)
	for index, prefix := range map[int]string{0: "data:image/webp;base64,", 2: "data:image/png;base64,"} {
		require.Equal(t, "input_image", content[index]["type"])
		require.True(t, strings.HasPrefix(content[index]["image_url"], prefix))
		data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(content[index]["image_url"], prefix))
		require.NoError(t, err)
		if index == 0 {
			require.Equal(t, picture.Bytes, data)
		} else {
			require.Equal(t, picture.AlphaPNG, data)
		}
	}
	require.Equal(t, "input_text", content[1]["type"])
	require.Contains(t, content[1]["text"], "preceding original")
	require.Contains(t, content[1]["text"], "black means transparent")
	picture.AlphaPNG = nil
	require.Len(t, modelImageContent(picture), 1)
}
