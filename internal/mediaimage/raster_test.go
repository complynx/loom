package mediaimage_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/mediaimage"
)

func TestAlphaCompanionPreservesPlaneAndBounds(t *testing.T) {
	t.Parallel()
	source := image.NewNRGBA64(image.Rect(0, 0, 3, 1))
	for x, alpha := range []uint16{0, 32768, 65535} {
		source.SetNRGBA64(x, 0, color.NRGBA64{R: 65535, A: alpha})
	}
	var original bytes.Buffer
	require.NoError(t, png.Encode(&original, source))
	mask, err := mediaimage.AlphaPNG(t.Context(), original.Bytes(), 1024)
	require.NoError(t, err)
	decoded, err := png.Decode(bytes.NewReader(mask))
	require.NoError(t, err)
	require.Equal(t, source.Bounds(), decoded.Bounds())
	for x, expected := range []uint32{0, 32768, 65535} {
		r, g, b, a := decoded.At(x, 0).RGBA()
		require.Equal(t, expected, r)
		require.Equal(t, r, g)
		require.Equal(t, r, b)
		require.Equal(t, uint32(65535), a)
	}
	_, err = mediaimage.AlphaPNG(t.Context(), original.Bytes(), 1)
	require.ErrorIs(t, err, mediaimage.ErrAlphaTooLarge)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = mediaimage.AlphaPNG(ctx, original.Bytes(), 1024)
	require.ErrorIs(t, err, context.Canceled)
}

func TestOpaqueImageNeedsNoAlphaCompanion(t *testing.T) {
	t.Parallel()
	picture := image.NewRGBA(image.Rect(0, 0, 1, 1))
	picture.SetRGBA(0, 0, color.RGBA{A: 255})
	var original bytes.Buffer
	require.NoError(t, png.Encode(&original, picture))
	mask, err := mediaimage.AlphaPNG(t.Context(), original.Bytes(), 0)
	require.NoError(t, err)
	require.Empty(t, mask)
}

func TestStillGIFMaskKeepsFrameOffsetOnLogicalCanvas(t *testing.T) {
	t.Parallel()
	for _, transparent := range []bool{true, false} {
		background := color.NRGBA{A: 255}
		if transparent {
			background.A = 0
		}
		palette := color.Palette{background, color.White}
		frame := image.NewPaletted(image.Rect(2, 2, 4, 4), palette)
		frame.SetColorIndex(2, 2, 1)
		var original bytes.Buffer
		require.NoError(t, gif.EncodeAll(&original, &gif.GIF{
			Image: []*image.Paletted{frame}, Delay: []int{0},
			Config: image.Config{ColorModel: palette, Width: 6, Height: 6},
		}))
		mask, err := mediaimage.AlphaPNG(t.Context(), original.Bytes(), 1024)
		require.NoError(t, err)
		if !transparent {
			require.Empty(t, mask)
			continue
		}
		decoded, err := png.Decode(bytes.NewReader(mask))
		require.NoError(t, err)
		require.Equal(t, image.Rect(0, 0, 6, 6), decoded.Bounds())
		for y := range 6 {
			for x := range 6 {
				expected := uint32(0)
				if x == 2 && y == 2 {
					expected = 65535
				}
				gray, _, _, alpha := decoded.At(x, y).RGBA()
				require.Equal(t, expected, gray, "pixel %d,%d", x, y)
				require.Equal(t, uint32(65535), alpha)
			}
		}
	}
}
