package fileimage_test

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/fileimage"
	"github.com/complynx/loom/internal/mediaimage"
)

func TestImageMethodsDoNotFlattenAnimation(t *testing.T) {
	t.Parallel()
	input := filepath.Join(t.TempDir(), "animation.gif")
	f, err := os.Create(input)
	require.NoError(t, err)
	frame := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White})
	require.NoError(t, gif.EncodeAll(f, &gif.GIF{Image: []*image.Paletted{frame, frame}, Delay: []int{100, 100}}))
	require.NoError(t, f.Close())
	for _, op := range []string{"image.info", "image.resize", "image.crop"} {
		_, out, _, _, processErr := fileimage.Process(t.Context(), op, json.RawMessage(`{}`), input, t.TempDir())
		require.ErrorIs(t, processErr, mediaimage.ErrAnimated)
		require.Empty(t, out)
	}
}

func TestResizeFitsModelAndNeverUpscales(t *testing.T) {
	t.Parallel()
	input := createImage(t, 2000, 1000)
	data, outputs, partial, cursor, err := fileimage.Process(
		t.Context(),
		"image.resize",
		json.RawMessage(`{}`),
		input,
		t.TempDir(),
	)
	require.NoError(t, err)
	require.False(t, partial)
	require.Empty(t, cursor)
	require.Len(t, outputs, 1)
	var info struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	}
	require.NoError(t, json.Unmarshal(data, &info))
	require.Equal(t, 1280, info.Width)
	require.Equal(t, 640, info.Height)
	_, outputs, _, _, err = fileimage.Process(
		t.Context(),
		"image.resize",
		json.RawMessage(`{"max_edge":4000}`),
		input,
		t.TempDir(),
	)
	require.NoError(t, err)
	picture := readImage(t, outputs[0].Path)
	require.Equal(t, image.Rect(0, 0, 2000, 1000), picture.Bounds())
}

func TestCropPreservesPixelsAndRejectsOutsideRectangle(t *testing.T) {
	t.Parallel()
	input := createImage(t, 8, 4)
	_, outputs, _, _, err := fileimage.Process(
		t.Context(),
		"image.crop",
		json.RawMessage(`{"x":4,"y":1,"width":3,"height":2}`),
		input,
		t.TempDir(),
	)
	require.NoError(t, err)
	cropped := readImage(t, outputs[0].Path)
	require.Equal(t, image.Rect(0, 0, 3, 2), cropped.Bounds())
	require.Equal(t, color.RGBA{R: 4, G: 1, A: 255}, color.RGBAModel.Convert(cropped.At(0, 0)))
	_, _, _, _, err = fileimage.Process(
		t.Context(),
		"image.crop",
		json.RawMessage(`{"x":7,"width":2,"height":1}`),
		input,
		t.TempDir(),
	)
	require.ErrorContains(t, err, "inside")
}

func TestRejectsMalformedCancelledAndPixelBomb(t *testing.T) {
	t.Parallel()
	input := createImage(t, 2, 2)
	for _, params := range []string{`{"unknown":1}`, `null`, `{} {}`, `{"max_edge":0}`, `{"width":-1}`} {
		_, _, _, _, err := fileimage.Process(t.Context(), "image.resize", json.RawMessage(params), input, t.TempDir())
		require.Error(t, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, _, _, err := fileimage.Process(ctx, "image.info", json.RawMessage(`{}`), input, t.TempDir())
	require.ErrorIs(t, err, context.Canceled)
	// JPEG SOF declares dimensions without allocating a decoded raster.
	bomb := filepath.Join(t.TempDir(), "bomb.jpg")
	header := []byte{
		0xff,
		0xd8,
		0xff,
		0xc0,
		0,
		17,
		8,
		0xff,
		0xff,
		0xff,
		0xff,
		3,
		1,
		0x11,
		0,
		2,
		0x11,
		0,
		3,
		0x11,
		0,
		0xff,
		0xda,
		0,
		12,
		3,
		1,
		0,
		2,
		0,
		3,
		0,
		0,
		63,
		0,
	}
	require.NoError(t, os.WriteFile(bomb, header, 0o600))
	_, _, _, _, err = fileimage.Process(t.Context(), "image.resize", json.RawMessage(`{}`), bomb, t.TempDir())
	require.ErrorContains(t, err, "pixel limit")
}

func TestJPEGOutputFlattensTransparency(t *testing.T) {
	t.Parallel()
	input := filepath.Join(t.TempDir(), "transparent.png")
	file, err := os.Create(input)
	require.NoError(t, err)
	require.NoError(t, png.Encode(file, image.NewNRGBA(image.Rect(0, 0, 2, 2))))
	require.NoError(t, file.Close())
	_, outputs, _, _, err := fileimage.Process(
		t.Context(),
		"image.resize",
		json.RawMessage(`{"format":"jpeg"}`),
		input,
		t.TempDir(),
	)
	require.NoError(t, err)
	r, g, b, _ := readImage(t, outputs[0].Path).At(0, 0).RGBA()
	require.Greater(t, r, uint32(65000))
	require.Greater(t, g, uint32(65000))
	require.Greater(t, b, uint32(65000))
}

func createImage(t *testing.T, width, height int) string {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			picture.SetRGBA(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), A: 255})
		}
	}
	path := filepath.Join(t.TempDir(), "input.png")
	file, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, png.Encode(file, picture))
	require.NoError(t, file.Close())
	return path
}

func readImage(t *testing.T, path string) image.Image {
	t.Helper()
	file, err := os.Open(path)
	require.NoError(t, err)
	defer file.Close()
	picture, _, err := image.Decode(file)
	require.NoError(t, err)
	return picture
}

func TestPNGTransformsPreserveAlpha(t *testing.T) {
	t.Parallel()
	picture := image.NewNRGBA(image.Rect(0, 0, 4, 2))
	for y := range 2 {
		picture.SetNRGBA(2, y, color.NRGBA{R: 200, G: 100, B: 50, A: 128})
		picture.SetNRGBA(3, y, color.NRGBA{A: 255})
	}
	input := filepath.Join(t.TempDir(), "alpha.png")
	f, err := os.Create(input)
	require.NoError(t, err)
	require.NoError(t, png.Encode(f, picture))
	require.NoError(t, f.Close())
	for _, op := range []string{"image.resize", "image.crop"} {
		_, outputs, _, _, processErr := fileimage.Process(t.Context(), op,
			json.RawMessage(`{"width":4,"height":2,"max_edge":2}`), input, t.TempDir())
		require.NoError(t, processErr)
		require.Len(t, outputs, 1)
		require.Equal(t, "image/png", outputs[0].MIME)
		out := readImage(t, outputs[0].Path)
		require.Equal(t, image.Rect(0, 0, 2, 1), out.Bounds())
		_, _, _, transparent := out.At(0, 0).RGBA()
		require.Zero(t, transparent)
		_, _, _, mixed := out.At(1, 0).RGBA()
		require.InDelta(t, 192*257, mixed, 257)
	}
}
