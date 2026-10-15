package fileimage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif" // Register first-frame GIF decoding.
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
)

const (
	pixelCenter     = 0.5
	maxInputBytes   = 20 << 20
	maxInputPixels  = 16_000_000
	maxOutputPixels = 4_000_000
	defaultEdge     = 1280
	defaultQuality  = 85
	channelMax      = 65535
	channelShift    = 8
)

// Output identifies a derived file. The caller publishes it as an opaque artifact handle.
type Output struct{ Name, MIME, Path string }

type parameters struct {
	X            int    `json:"x"`
	Y            int    `json:"y"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	MaxEdge      int    `json:"max_edge"`
	AllowUpscale bool   `json:"allow_upscale"`
	Format       string `json:"format"`
	Quality      int    `json:"quality"`
}

type result struct {
	Width              int    `json:"width"`
	Height             int    `json:"height"`
	Format             string `json:"format"`
	OrientationApplied bool   `json:"orientation_applied"`
	FirstFrameOnly     bool   `json:"first_frame_only"`
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

// Process inspects or derives an image without returning image bytes to the script sandbox.
// Coordinates use the stored raster: EXIF orientation is not applied. GIF derivation uses its first frame.
func Process(
	ctx context.Context,
	operation string,
	params json.RawMessage,
	inputPath, outputDir string,
) (json.RawMessage, []Output, bool, string, error) {
	data, outputs, err := process(ctx, operation, params, inputPath, outputDir)
	return data, outputs, false, "", err
}

func process(
	ctx context.Context,
	operation string,
	params json.RawMessage,
	inputPath, outputDir string,
) (json.RawMessage, []Output, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if operation != "image.info" && operation != "image.resize" && operation != "image.crop" {
		return nil, nil, errors.New("unsupported image operation")
	}
	p, err := parseParameters(params)
	if err != nil {
		return nil, nil, err
	}
	file, err := os.Open(inputPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open image: %w", err)
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return nil, nil, fmt.Errorf("stat image: %w", err)
	}
	if !stat.Mode().IsRegular() || stat.Size() > maxInputBytes {
		return nil, nil, errors.New("image must be a regular file at most 20 MiB")
	}
	config, format, err := image.DecodeConfig(contextReader{ctx, io.LimitReader(file, maxInputBytes+1)})
	if err != nil {
		return nil, nil, fmt.Errorf("decode image header: %w", err)
	}
	if err = checkDimensions(config.Width, config.Height, maxInputPixels); err != nil {
		return nil, nil, err
	}
	if operation == "image.info" {
		data, marshalErr := json.Marshal(result{Width: config.Width, Height: config.Height, Format: format})
		return data, nil, marshalErr
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, nil, fmt.Errorf("seek image: %w", err)
	}
	source, _, err := image.Decode(contextReader{ctx, io.LimitReader(file, maxInputBytes+1)})
	if err != nil {
		return nil, nil, fmt.Errorf("decode image: %w", err)
	}
	rect, width, height, err := outputBounds(operation, p, source.Bounds())
	if err != nil {
		return nil, nil, err
	}
	scaled, err := scale(ctx, source, rect, width, height)
	if err != nil {
		return nil, nil, err
	}
	output, err := writeImage(ctx, scaled, outputDir, p)
	if err != nil {
		return nil, nil, err
	}
	data, err := json.Marshal(result{Width: width, Height: height, Format: p.Format, FirstFrameOnly: format == "gif"})
	return data, []Output{output}, err
}

func parseParameters(raw json.RawMessage) (parameters, error) {
	p := parameters{MaxEdge: defaultEdge, Format: "png", Quality: defaultQuality}
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		return p, fmt.Errorf("image parameters: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return p, errors.New("image parameters must contain one JSON object")
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return p, errors.New("image parameters must be an object")
	}
	if p.MaxEdge < 1 || p.MaxEdge > maxOutputPixels || p.Width < 0 || p.Height < 0 || p.Quality < 1 || p.Quality > 100 {
		return p, errors.New("invalid image dimensions or quality")
	}
	if p.Format != "png" && p.Format != "jpeg" {
		return p, errors.New("output format must be png or jpeg")
	}
	return p, nil
}

func checkDimensions(width, height, limit int) error {
	if width < 1 || height < 1 || width > limit || height > limit/width {
		return errors.New("image exceeds pixel limit")
	}
	return nil
}

func outputBounds(operation string, p parameters, bounds image.Rectangle) (image.Rectangle, int, int, error) {
	rect := bounds
	if operation == "image.crop" {
		if p.X < 0 || p.Y < 0 || p.Width < 1 || p.Height < 1 || p.X > bounds.Dx() || p.Y > bounds.Dy() ||
			p.Width > bounds.Dx()-p.X ||
			p.Height > bounds.Dy()-p.Y {
			return rect, 0, 0, errors.New("crop rectangle must lie inside the image")
		}
		rect = image.Rect(bounds.Min.X+p.X, bounds.Min.Y+p.Y, bounds.Min.X+p.X+p.Width, bounds.Min.Y+p.Y+p.Height)
	}
	ratio := math.Min(float64(p.MaxEdge)/float64(rect.Dx()), float64(p.MaxEdge)/float64(rect.Dy()))
	if operation == "image.resize" {
		if p.Width > 0 {
			ratio = math.Min(ratio, float64(p.Width)/float64(rect.Dx()))
		}
		if p.Height > 0 {
			ratio = math.Min(ratio, float64(p.Height)/float64(rect.Dy()))
		}
	}
	if !p.AllowUpscale {
		ratio = math.Min(ratio, 1)
	}
	width, height := max(1, int(float64(rect.Dx())*ratio)), max(1, int(float64(rect.Dy())*ratio))
	if err := checkDimensions(width, height, maxOutputPixels); err != nil {
		return rect, 0, 0, err
	}
	return rect, width, height, nil
}

func scale(ctx context.Context, source image.Image, rect image.Rectangle, width, height int) (*image.RGBA, error) {
	output := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sy := max(0, (float64(y)+pixelCenter)*float64(rect.Dy())/float64(height)-pixelCenter)
		y0 := min(int(sy), rect.Dy()-1)
		y1 := min(y0+1, rect.Dy()-1)
		for x := range width {
			sx := max(0, (float64(x)+pixelCenter)*float64(rect.Dx())/float64(width)-pixelCenter)
			x0 := min(int(sx), rect.Dx()-1)
			x1 := min(x0+1, rect.Dx()-1)
			pixels := [4]color.Color{
				source.At(rect.Min.X+x0, rect.Min.Y+y0),
				source.At(rect.Min.X+x1, rect.Min.Y+y0),
				source.At(rect.Min.X+x0, rect.Min.Y+y1),
				source.At(rect.Min.X+x1, rect.Min.Y+y1),
			}
			output.SetRGBA(x, y, interpolate(pixels, sx-float64(x0), sy-float64(y0)))
		}
	}
	return output, nil
}

func interpolate(pixels [4]color.Color, fx, fy float64) color.RGBA {
	weights := [4]float64{(1 - fx) * (1 - fy), fx * (1 - fy), (1 - fx) * fy, fx * fy}
	var channels [4]float64
	for i, pixel := range pixels {
		r, g, b, a := pixel.RGBA()
		channels[0] += float64(r) * weights[i]
		channels[1] += float64(g) * weights[i]
		channels[2] += float64(b) * weights[i]
		channels[3] += float64(a) * weights[i]
	}
	return color.RGBA{
		R: channel(channels[0]),
		G: channel(channels[1]),
		B: channel(channels[2]),
		A: channel(channels[3]),
	}
}

func channel(value float64) uint8 {
	return uint8(min(channelMax, max(0, int(math.Round(value)))) >> channelShift)
}

func writeImage(ctx context.Context, picture image.Image, directory string, p parameters) (Output, error) {
	file, err := os.CreateTemp(directory, "image-*."+p.Format)
	if err != nil {
		return Output{}, fmt.Errorf("create derived image: %w", err)
	}
	path := file.Name()
	if err = encodeImage(ctx, file, picture, p); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return Output{}, fmt.Errorf("encode derived image: %w", err)
	}
	if err = file.Close(); err != nil {
		_ = os.Remove(path)
		return Output{}, fmt.Errorf("close derived image: %w", err)
	}
	return Output{Name: "image." + p.Format, MIME: "image/" + p.Format, Path: path}, nil
}

func encodeImage(ctx context.Context, writer io.Writer, picture image.Image, p parameters) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.Format == "jpeg" {
		opaque := image.NewRGBA(picture.Bounds())
		draw.Draw(opaque, opaque.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
		draw.Draw(opaque, opaque.Bounds(), picture, picture.Bounds().Min, draw.Over)
		return jpeg.Encode(contextWriter{ctx, writer}, opaque, &jpeg.Options{Quality: p.Quality})
	}
	return png.Encode(contextWriter{ctx, writer}, picture)
}

type contextWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
func (w contextWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.writer.Write(p)
}
