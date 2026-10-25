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

	"github.com/complynx/loom/internal/fileimage"
	"github.com/complynx/loom/internal/fileproc"
	"github.com/complynx/loom/internal/mediaimage"
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

func TestNativeWebPFrameSupportsImageTransforms(t *testing.T) {
	t.Parallel()
	needCommands(t, "ffmpeg", "loom-animation")
	input := filepath.Join(t.TempDir(), "input")
	require.NoError(t, os.WriteFile(input, readAlphaFixture(t, "tgs"), 0600))
	frame, err := fileproc.Process(
		t.Context(),
		fileproc.Request{Operation: "video.frame", Params: json.RawMessage(`{"width":32}`)},
		input,
		t.TempDir(),
	)
	require.NoError(t, err)
	require.Len(t, frame.Outputs, 1)
	for _, operation := range []string{"image.resize", "image.crop"} {
		for _, format := range []string{"png", "jpeg"} {
			params, marshalErr := json.Marshal(map[string]any{"format": format, "width": 32, "height": 32})
			require.NoError(t, marshalErr)
			_, outputs, _, _, processErr := fileimage.Process(
				t.Context(),
				operation,
				params,
				frame.Outputs[0].Path,
				t.TempDir(),
			)
			require.NoError(t, processErr)
			require.Len(t, outputs, 1)
			decoded := decodeFrame(t, outputs[0].Path)
			r, g, b, a := decoded.At(24, 8).RGBA()
			if format == "png" {
				require.Zero(t, a)
			} else {
				require.Greater(t, r, uint32(65000))
				require.Greater(t, g, uint32(65000))
				require.Greater(t, b, uint32(65000))
			}
		}
	}
	data, err := os.ReadFile(frame.Outputs[0].Path)
	require.NoError(t, err)
	mask, err := mediaimage.AlphaPNG(t.Context(), data, 1<<20)
	require.NoError(t, err)
	require.NotEmpty(t, mask)
}

func TestAnimationBackgroundChoices(t *testing.T) {
	t.Parallel()
	needCommands(t, "ffmpeg", "ffprobe", "loom-animation")
	for _, format := range []string{"tgs", "apng", "vp9.webm", "animated.avif", "mng"} {
		input := filepath.Join(t.TempDir(), "input")
		require.NoError(t, os.WriteFile(input, readAlphaFixture(t, format), 0600))
		for _, background := range []string{"white", "#123456", "checkerboard"} {
			params, err := json.Marshal(map[string]any{"width": 32, "background": background, "lossless": true})
			require.NoError(t, err)
			r, processErr := fileproc.Process(
				t.Context(),
				fileproc.Request{Operation: "video.frame", Params: params},
				input,
				t.TempDir(),
			)
			require.NoError(t, processErr, format+" "+background)
			decoded := decodeFrame(t, r.Outputs[0].Path)
			red, green, blue, alpha := decoded.At(24, 8).RGBA()
			require.Equal(t, uint32(65535), alpha)
			switch background {
			case "white":
				require.Equal(t, uint32(65535), red)
				require.Equal(t, red, green)
				require.Equal(t, red, blue)
			case "#123456":
				require.InDelta(t, 18*257, red, 257)
				require.InDelta(t, 52*257, green, 257)
				require.InDelta(t, 86*257, blue, 257)
			case "checkerboard":
				require.InDelta(t, 204*257, red, 257)
				require.Equal(t, red, green)
				require.Equal(t, red, blue)
			}
		}
	}
}

func TestImageTransformsRejectAnimatedWebP(t *testing.T) {
	t.Parallel()
	input := filepath.Join(t.TempDir(), "input")
	require.NoError(t, os.WriteFile(input, animatedWebP, 0600))
	for _, operation := range []string{"image.info", "image.resize", "image.crop"} {
		_, outputs, _, _, err := fileimage.Process(t.Context(), operation, json.RawMessage(`{}`), input, t.TempDir())
		require.ErrorIs(t, err, mediaimage.ErrAnimated)
		require.Empty(t, outputs)
	}
}
