package fileproc

import (
	"bytes"
	_ "embed"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

//go:embed testdata/alpha-animated.avif
var auxiliaryAlphaFixture []byte

func TestAuxiliaryAlphaRequiresTypeAndColorReference(t *testing.T) {
	t.Parallel()
	data := auxiliaryAlphaFixture
	for _, alpha := range []bool{true, false} {
		content := data
		if !alpha {
			content = bytes.ReplaceAll(data, []byte("auxiliary:alpha"), []byte("auxiliary:depth"))
		}
		input := filepath.Join(t.TempDir(), "input")
		require.NoError(t, os.WriteFile(input, content, 0600))
		probe, identifyErr := identifyAlphaTracks(input, videoProbe{ImageSequence: true, Streams: []videoStream{
			{
				ID:        "0x2",
				Index:     1,
				CodecType: videoCodecType,
				Frames:    "2",
				Width:     32,
				Height:    16,
				Tags:      map[string]string{"title": "Alpha"},
			},
			{
				ID:        "0x1",
				Index:     0,
				CodecType: videoCodecType,
				Frames:    "2",
				Width:     32,
				Height:    16,
				Tags:      map[string]string{"title": "Color"},
			},
		}})
		require.NoError(t, identifyErr)
		require.Equal(t, alpha, probe.Streams[0].Alpha)
		if !alpha {
			require.Equal(t, -1, probe.alphaStream(1))
			continue
		}
		selected, selectErr := probe.visualStream()
		require.NoError(t, selectErr)
		require.Equal(t, 1, selected)
		require.Equal(t, 0, probe.alphaStream(selected))
		probe.Streams[selected].ID = "0x3"
		require.Equal(t, -1, probe.alphaStream(selected))
	}
}

func TestFrameEncodingRejectsInvalidQuality(t *testing.T) {
	t.Parallel()
	for _, quality := range []int{-1, 101} {
		_, err := videoOptions(VideoOptions{Quality: quality})
		require.Error(t, err)
	}
}

func TestImageSequenceBoxBounds(t *testing.T) {
	t.Parallel()
	for _, data := range [][]byte{
		{0, 0, 0},
		{0, 0, 0, 4, 'm', 'o', 'o', 'v'},
		{0, 0, 0, 1, 'm', 'o', 'o', 'v'},
		{255, 255, 255, 255, 'm', 'o', 'o', 'v'},
	} {
		_, err := childBoxes(data, "moov", "trak")
		require.Error(t, err)
	}
}
