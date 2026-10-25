package fileproc_test

import (
	"bytes"
	"compress/gzip"
	"embed"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/fileproc"
)

// Synthetic two-frame animations: opaque color, half-transparent green, then clear pixels.
//
//go:embed testdata/alpha.* testdata/alpha-*.webm testdata/alpha-animated.avif
var alphaFixtures embed.FS

func TestAnimationAlphaAndEncodingOptions(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"apng", "jxl", "mng", "json", "tgs", "heics", "vp8.webm", "vp9.webm", "animated.avif"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			needCommands(t, "ffmpeg", "ffprobe")
			if format == "mng" || format == "json" || format == "tgs" {
				needCommands(t, "loom-animation")
			}
			data := readAlphaFixture(t, format)
			input := filepath.Join(t.TempDir(), "input")
			require.NoError(t, os.WriteFile(input, data, 0600))
			for _, encoding := range []string{``, `,"quality":75`, `,"lossless":true`} {
				for _, operation := range []string{"video.frame", "video.storyboard"} {
					r, processErr := fileproc.Process(t.Context(), fileproc.Request{Operation: operation,
						Params: json.RawMessage(`{"width":32,"interval":1` + encoding + `}`)}, input, t.TempDir())
					require.NoError(t, processErr)
					require.NotEmpty(t, r.Outputs)
					assertAlphaOutputs(t, format, r.Outputs)
				}
			}
		})
	}
}

func gzipFixture(t *testing.T, data []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w := gzip.NewWriter(&out)
	_, err := w.Write(data)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return out.Bytes()
}

func TestVectorFrameRenderSize(t *testing.T) {
	t.Parallel()
	needCommands(t, "ffmpeg", "loom-animation")
	data, err := alphaFixtures.ReadFile("testdata/alpha.json")
	require.NoError(t, err)
	input := filepath.Join(t.TempDir(), "input")
	require.NoError(t, os.WriteFile(input, gzipFixture(t, data), 0600))
	for _, size := range []int{16, 96} {
		params, marshalErr := json.Marshal(map[string]any{"width": size, "lossless": true})
		require.NoError(t, marshalErr)
		r, processErr := fileproc.Process(
			t.Context(),
			fileproc.Request{Operation: "video.frame", Params: params},
			input,
			t.TempDir(),
		)
		require.NoError(t, processErr)
		require.Len(t, r.Outputs, 1)
		require.Equal(t, image.Rect(0, 0, size, size), decodeFrame(t, r.Outputs[0].Path).Bounds())
	}
}

func decodeFrame(t *testing.T, path string) image.Image {
	t.Helper()
	data, err := exec.CommandContext(t.Context(), "ffmpeg", "-v", "error", "-i", path,
		"-frames:v", "1", "-f", "image2pipe", "-c:v", "png", "-").Output()
	require.NoError(t, err)
	im, err := png.Decode(bytes.NewReader(data))
	require.NoError(t, err)
	return im
}

func assertAlphaOutputs(t *testing.T, format string, outputs []fileproc.Output) {
	t.Helper()
	for index, out := range outputs {
		require.Equal(t, "image/webp", out.MIME)
		im := decodeFrame(t, out.Path)
		require.Equal(t, 32, im.Bounds().Dx())
		_, _, _, transparent := im.At(24, 8).RGBA()
		require.Zero(t, transparent)
		_, _, _, partial := im.At(12, 8).RGBA()
		expected := 128
		switch {
		case format == "mng" && index > 0:
			expected = 192
		case format == "json" || format == "tgs":
			expected = 64
		}
		require.InDelta(t, expected*257, partial, 2*257)
	}
}

func readAlphaFixture(t *testing.T, format string) []byte {
	t.Helper()
	name := "alpha." + format
	switch format {
	case "tgs":
		name = "alpha.json"
	case "vp8.webm", "vp9.webm", "animated.avif":
		name = "alpha-" + format
	}
	data, err := alphaFixtures.ReadFile("testdata/" + name)
	require.NoError(t, err)
	if format == "tgs" {
		return gzipFixture(t, data)
	}
	return data
}
