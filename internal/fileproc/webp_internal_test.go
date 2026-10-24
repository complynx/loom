package fileproc

import (
	_ "embed"
	"encoding/json"
	"image/jpeg"
	"os"
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
		f, err := os.Open(r.Outputs[0].Path)
		require.NoError(t, err)
		im, err := jpeg.Decode(f)
		require.NoError(t, f.Close())
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
