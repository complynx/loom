package fileproc

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/mediaimage"
)

//go:embed testdata/animated.webp
var compositionWebP []byte

func TestWebPCompositionAndDisposal(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	for _, dispose := range []bool{false, true} {
		a, err := mediaimage.ParseWebP(compositionWebP)
		require.NoError(t, err)
		require.Len(t, a.Frames, 2)
		a.Width = 32
		a.Background = [4]byte{0, 255, 0, 255}
		a.Frames[0].Dispose = dispose
		a.Frames[1].X = 16
		r, err := webPResult(
			t.Context(),
			Request{Operation: "video.frame", Params: json.RawMessage(`{"timestamp":1.5,"width":32}`)},
			a,
			t.TempDir(),
		)
		require.NoError(t, err)
		require.Len(t, r.Outputs, 1)
		im := decodeWebPFrameFile(t, r.Outputs[0].Path)
		require.NoError(t, err)
		red, green, _, _ := im.At(8, 8).RGBA()
		if dispose {
			require.Greater(t, green, uint32(50000))
			require.Less(t, red, uint32(5000))
		} else {
			require.Greater(t, red, uint32(50000))
		}
		_, _, blue, _ := im.At(24, 8).RGBA()
		require.Greater(t, blue, uint32(50000))
	}
}

func TestWebPFramesPreserveAlpha(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	for _, alpha := range []byte{0, 128} {
		a, err := mediaimage.ParseWebP(compositionWebP)
		require.NoError(t, err)
		a.Width = 32
		a.Background = [4]byte{0, 255, 0, alpha}
		for _, operation := range []string{"video.frame", "video.storyboard"} {
			r, processErr := webPResult(t.Context(), Request{Operation: operation,
				Params: json.RawMessage(`{"interval":1,"width":32}`)}, a, t.TempDir())
			require.NoError(t, processErr)
			require.NotEmpty(t, r.Outputs)
			for _, out := range r.Outputs {
				require.Equal(t, "image/webp", out.MIME)
				im := decodeWebPFrameFile(t, out.Path)

				_, _, _, got := im.At(24, 8).RGBA()
				require.Equal(t, uint32(alpha)*257, got)
				_, _, _, got = im.At(8, 8).RGBA()
				require.Equal(t, uint32(65535), got)
			}
		}
	}
}

func decodeWebPFrameFile(t *testing.T, path string) image.Image {
	t.Helper()
	data, err := exec.CommandContext(t.Context(), "ffmpeg", "-v", "error", "-i", path, "-frames:v", "1", "-f", "image2pipe", "-c:v", "png", "-").
		Output()
	require.NoError(t, err)
	im, err := png.Decode(bytes.NewReader(data))
	require.NoError(t, err)
	return im
}

func TestLosslessFramePreservesDecodedColor(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	canvas := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	expected := color.NRGBA{R: 173, G: 89, B: 37, A: 128}
	canvas.SetNRGBA(8, 8, expected)
	out, err := saveWebPSample(
		t.Context(),
		canvas,
		VideoOptions{Width: 16, Quality: 90, Lossless: true},
		0,
		t.TempDir(),
	)
	require.NoError(t, err)
	decoded := decodeWebPFrameFile(t, out.Path)
	require.Equal(t, expected, color.NRGBAModel.Convert(decoded.At(8, 8)))
}
