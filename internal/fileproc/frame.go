package fileproc

import (
	"context"
	"fmt"
	"strconv"

	"github.com/complynx/loom/internal/mediaimage"
)

const webPMIME = "image/webp"

// WebP stores alpha losslessly even when color uses the requested lossy quality.
func webPEncodingOptions(p VideoOptions) []string {
	lossless := "0"
	if p.Lossless {
		lossless = "1"
	}
	return []string{"-c:v", "libwebp", "-quality", strconv.Itoa(p.Quality), "-lossless", lossless}
}

func encodeWebPFrame(ctx context.Context, input, output string, p VideoOptions) error {
	args := []string{
		"-v",
		"error",
		"-nostdin",
		"-y",
		"-protocol_whitelist",
		"file,pipe",
		"-i",
		input,
		"-frames:v",
		"1",
		"-vf",
		fmt.Sprintf(
			"scale=%d:%d:force_original_aspect_ratio=decrease",
			p.Width,
			p.Width,
		) + frameBackgroundFilter(
			p.Background,
		),
	}
	args = append(args, webPEncodingOptions(p)...)
	args = append(args, output)
	_, err := command(ctx, "ffmpeg", args...)
	return err
}

// The background is validated by videoOptions before it can enter a filter graph.
func frameBackgroundFilter(value string) string {
	background, err := mediaimage.ParseBackground(value)
	if err != nil || !background.Enabled {
		return ""
	}
	fill := fmt.Sprintf(
		"drawbox=color=0x%02x%02x%02x:t=fill:replace=1",
		background.Color.R,
		background.Color.G,
		background.Color.B,
	)
	if background.Checkerboard {
		pattern := fmt.Sprintf(
			"if(mod(floor(X/%d)+floor(Y/%d),2),%d,%d)",
			mediaimage.CheckerSize,
			mediaimage.CheckerSize,
			mediaimage.CheckerDark,
			mediaimage.CheckerLight,
		)
		fill = fmt.Sprintf("geq=r='%s':g='%s':b='%s':a=255", pattern, pattern, pattern)
	}
	return ",format=rgba,split[loom_fg][loom_bg];[loom_bg]" + fill + "[loom_matte];[loom_matte][loom_fg]overlay=shortest=1:format=auto"
}
