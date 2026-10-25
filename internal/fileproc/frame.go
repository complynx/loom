package fileproc

import (
	"context"
	"fmt"
	"strconv"
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
	args := []string{"-v", "error", "-nostdin", "-y", "-protocol_whitelist", "file,pipe", "-i", input,
		"-frames:v", "1", "-vf", fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", p.Width, p.Width)}
	args = append(args, webPEncodingOptions(p)...)
	args = append(args, output)
	_, err := command(ctx, "ffmpeg", args...)
	return err
}
