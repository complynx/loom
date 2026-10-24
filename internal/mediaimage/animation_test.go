package mediaimage_test

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/gif"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/mediaimage"
)

func TestDetectAnimationWithoutDecodingPixels(t *testing.T) {
	t.Parallel()
	for _, frames := range []int{1, 2} {
		var b bytes.Buffer
		a := &gif.GIF{}
		for range frames {
			a.Image = append(
				a.Image,
				image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White}),
			)
			a.Delay = append(a.Delay, 10)
		}
		require.NoError(t, gif.EncodeAll(&b, a))
		animated, err := mediaimage.Animated(b.Bytes())
		require.NoError(t, err)
		require.Equal(t, frames > 1, animated)
		_, err = mediaimage.Animated(b.Bytes()[:15])
		require.Error(t, err)
	}
	for _, flag := range []byte{0, 2} {
		webp := make([]byte, 30)
		copy(webp, "RIFF")
		binary.LittleEndian.PutUint32(webp[4:], 22)
		copy(webp[8:], "WEBPVP8X")
		binary.LittleEndian.PutUint32(webp[16:], 10)
		webp[20] = flag
		animated, err := mediaimage.Animated(webp)
		require.NoError(t, err)
		require.Equal(t, flag == 2, animated)
		_, err = mediaimage.Animated(webp[:29])
		require.Error(t, err)
	}
}
