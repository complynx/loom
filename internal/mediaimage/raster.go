package mediaimage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"  // Register still GIF decoding.
	_ "image/jpeg" // Register JPEG header decoding.
	"image/png"
	"io"

	_ "golang.org/x/image/webp" // Register still WebP decoding, including alpha.
)

const maxMaskPixels = 16_000_000
const opaqueAlpha = 65535

var ErrAlphaTooLarge = errors.New("alpha mask exceeds image or byte limit")

// AlphaPNG returns an opaque grayscale alpha plane: black is transparent and
// white is opaque. Opaque originals need no companion. Original bytes are unchanged.
func AlphaPNG(ctx context.Context, data []byte, maxBytes int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode alpha image header: %w", err)
	}
	if format == "jpeg" {
		return nil, nil
	}
	if config.Width < 1 || config.Height < 1 || config.Width > maxMaskPixels/config.Height {
		return nil, ErrAlphaTooLarge
	}
	picture, _, err := image.Decode(rasterReader{Context: ctx, Reader: bytes.NewReader(data)})
	if err != nil {
		return nil, fmt.Errorf("decode alpha image: %w", err)
	}
	canvas := image.Rect(0, 0, config.Width, config.Height)
	if opaqueCanvas(picture, canvas) {
		return nil, nil
	}
	mask := image.NewGray16(canvas)
	for y := range config.Height {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		for x := range config.Width {
			_, _, _, alpha := picture.At(x, y).RGBA()
			mask.SetGray16(x, y, color.Gray16{Y: uint16(alpha)}) // #nosec G115 -- Color.RGBA returns 16-bit channels.
		}
	}
	var out bytes.Buffer
	if err = png.Encode(&maskWriter{ctx: ctx, remaining: maxBytes, out: &out}, mask); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func opaqueCanvas(picture image.Image, canvas image.Rectangle) bool {
	if picture.Bounds() != canvas {
		// A GIF's single frame can cover only part of its logical screen. With
		// transparency, the uncovered canvas also belongs in the alpha mask.
		paletted, ok := picture.(*image.Paletted)
		if !ok {
			return false
		}
		for _, c := range paletted.Palette {
			if _, _, _, alpha := c.RGBA(); alpha != opaqueAlpha {
				return false
			}
		}
		return true
	}
	opaque, ok := picture.(interface{ Opaque() bool })
	return ok && opaque.Opaque()
}

type rasterReader struct {
	context.Context
	io.Reader
}

func (r rasterReader) Read(p []byte) (int, error) {
	if err := r.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

type maskWriter struct {
	ctx       context.Context
	remaining int64
	out       *bytes.Buffer
}

func (w *maskWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(p)) > w.remaining {
		return 0, ErrAlphaTooLarge
	}
	w.remaining -= int64(len(p))
	return w.out.Write(p)
}
