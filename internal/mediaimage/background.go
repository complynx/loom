package mediaimage

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"strconv"
)

const (
	CheckerSize           = 8
	CheckerLight    uint8 = 204
	CheckerDark     uint8 = 153
	opaqueChannel         = 255
	redChannelShift       = 16
)

type Background struct {
	Enabled      bool
	Checkerboard bool
	Color        color.RGBA
}

// ParseBackground accepts no matte, an opaque RGB color, or a fixed gray checkerboard.
func ParseBackground(value string) (Background, error) {
	switch value {
	case "", "transparent":
		return Background{}, nil
	case "checkerboard":
		return Background{Enabled: true, Checkerboard: true}, nil
	case "white":
		value = "#ffffff"
	case "black":
		value = "#000000"
	}
	if len(value) != 7 || value[0] != '#' {
		return Background{}, errors.New("background must be transparent, white, black, #RRGGBB or checkerboard")
	}
	rgb, err := strconv.ParseUint(value[1:], 16, 24)
	if err != nil {
		return Background{}, errors.New("background must be a valid #RRGGBB color")
	}
	return Background{Enabled: true, Color: color.RGBA{
		R: uint8(
			rgb >> redChannelShift,
		),
		G: uint8(rgb >> 8 & opaqueChannel),
		B: uint8(rgb & opaqueChannel),
		A: opaqueChannel,
	}}, nil
}

// Composite places the decoded raster on a requested matte. Empty preserves alpha.
func Composite(ctx context.Context, picture image.Image, value string) (image.Image, error) {
	background, err := ParseBackground(value)
	if err != nil || !background.Enabled {
		return picture, err
	}
	output := image.NewRGBA(picture.Bounds())
	for y := output.Bounds().Min.Y; y < output.Bounds().Max.Y; y++ {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		for x := output.Bounds().Min.X; x < output.Bounds().Max.X; x++ {
			c := background.Color
			if background.Checkerboard {
				gray := CheckerLight
				if (x/CheckerSize+y/CheckerSize)%2 != 0 {
					gray = CheckerDark
				}
				c = color.RGBA{R: gray, G: gray, B: gray, A: opaqueChannel}
			}
			output.SetRGBA(x, y, c)
		}
	}
	draw.Draw(output, output.Bounds(), picture, picture.Bounds().Min, draw.Over)
	return output, nil
}
