package fileproc

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/complynx/loom/internal/mediaimage"
)

const (
	videoInfoOperation    = "video.info"
	videoAudioOperation   = "video.audio"
	durationKey           = "duration"
	minAnimationInterval  = 0.1
	millisecondsPerSecond = 1000
	timestampTolerance    = 1e-9
)

// Animated WebP is composed here because FFmpeg only decodes its static frames.
func processAnimatedWebP(ctx context.Context, req Request, input, dir string) (Result, bool, error) {
	f, err := os.Open(input) // #nosec G703 -- input is the worker-owned staged file, not a caller path.
	if err != nil {
		return Result{}, false, err
	}
	defer f.Close()
	var header [12]byte
	if _, err = io.ReadFull(f, header[:]); err != nil || string(header[:4]) != "RIFF" || string(header[8:]) != "WEBP" {
		return Result{}, false, nil
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return Result{}, true, err
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxInputBytes+1))
	if err != nil {
		return Result{}, true, err
	}
	if int64(len(data)) > MaxInputBytes {
		return Result{}, true, errors.New("WebP exceeds input limit")
	}
	animated, err := mediaimage.Animated(data)
	if err != nil || !animated {
		return Result{}, false, err
	}
	a, err := mediaimage.ParseWebP(data)
	if err != nil {
		return Result{}, true, err
	}
	r, err := webPResult(ctx, req, a, dir)
	return r, true, err
}

func webPResult(ctx context.Context, req Request, a mediaimage.WebPAnimation, dir string) (Result, error) {
	var raw VideoOptions
	if err := decodeOptions(req.Params, &raw); err != nil {
		return Result{}, err
	}
	p, err := videoOptions(raw)
	if err != nil {
		return Result{}, err
	}
	duration := 0.0
	for _, f := range a.Frames {
		duration += float64(f.DelayMS) / millisecondsPerSecond
	}
	if req.Operation == videoInfoOperation {
		return dataResult(
			map[string]any{
				"format": map[string]any{"format_name": "webp", durationKey: preciseSeconds(duration)},
				"streams": []any{
					map[string]any{
						"codec_name": "webp",
						"codec_type": videoCodecType,
						"width":      a.Width,
						"height":     a.Height,
						durationKey:  preciseSeconds(duration),
					},
				},
			},
		)
	}
	if req.Operation == videoAudioOperation {
		return dataResult(map[string]any{hasAudioKey: false})
	}
	if raw.Interval == 0 {
		p.Interval = math.Max(minAnimationInterval, duration/float64(p.MaxFrames))
	}
	times, partial, next, err := videoTimestamps(req, p, duration)
	if err != nil {
		return Result{}, err
	}
	for _, at := range times {
		if at >= duration {
			return Result{}, errors.New("frame timestamp exceeds duration")
		}
	}
	r, err := dataResult(map[string]any{timestampsKey: times, durationKey: duration, hasAudioKey: false})
	if err != nil {
		return Result{}, err
	}
	r.Outputs, err = composeWebPSamples(ctx, a, times, p.Width, dir)
	if err != nil {
		return Result{}, err
	}
	r.Partial = partial
	if partial {
		r.NextCursor = preciseSeconds(next)
	}
	return r, nil
}

func composeWebPSamples(
	ctx context.Context,
	a mediaimage.WebPAnimation,
	times []float64,
	width int,
	dir string,
) ([]Output, error) {
	var outputs []Output
	canvas := image.NewNRGBA(image.Rect(0, 0, a.Width, a.Height))
	bg := image.NewUniform(color.NRGBA{R: a.Background[0], G: a.Background[1], B: a.Background[2], A: a.Background[3]})
	draw.Draw(canvas, canvas.Bounds(), bg, image.Point{}, draw.Src)
	at, sample := 0.0, 0
	for _, frame := range a.Frames {
		if sample == len(times) {
			break
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		picture, decodeErr := decodeWebPFrame(ctx, frame, dir)
		if decodeErr != nil {
			return nil, decodeErr
		}
		rect := image.Rect(frame.X, frame.Y, frame.X+frame.Width, frame.Y+frame.Height)
		op := draw.Over
		if frame.Replace {
			op = draw.Src
		}
		draw.Draw(canvas, rect, picture, picture.Bounds().Min, op)
		end := at + float64(frame.DelayMS)/millisecondsPerSecond
		for sample < len(times) && times[sample] < end-timestampTolerance {
			out, saveErr := saveWebPSample(ctx, canvas, width, sample, dir)
			if saveErr != nil {
				return nil, saveErr
			}
			outputs = append(outputs, out)
			sample++
		}
		if frame.Dispose {
			draw.Draw(canvas, rect, bg, image.Point{}, draw.Src)
		}
		at = end
	}
	return outputs, nil
}

func decodeWebPFrame(ctx context.Context, f mediaimage.WebPFrame, dir string) (image.Image, error) {
	input, output := filepath.Join(dir, "webp-frame.webp"), filepath.Join(dir, "webp-frame.png")
	defer os.Remove(input)
	defer os.Remove(output)
	if err := os.WriteFile(input, mediaimage.StillWebP(f), 0600); err != nil {
		return nil, err
	}
	if _, err := command(
		ctx,
		"ffmpeg",
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
		output,
	); err != nil {
		return nil, err
	}
	file, err := os.Open(output)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	config, err := png.DecodeConfig(file)
	if err != nil {
		return nil, err
	}
	if config.Width != f.Width || config.Height != f.Height {
		return nil, errors.New("WebP frame dimensions mismatch")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return png.Decode(file)
}

func saveWebPSample(ctx context.Context, canvas image.Image, width, index int, dir string) (Output, error) {
	// Reuse FFmpeg's bounded aspect-preserving scaling for storyboard outputs.
	input := filepath.Join(dir, "webp-canvas.png")
	defer os.Remove(input)
	f, err := os.Create(input)
	if err != nil {
		return Output{}, err
	}
	err = png.Encode(f, canvas)
	closeErr := f.Close()
	if err != nil {
		return Output{}, err
	}
	if closeErr != nil {
		return Output{}, closeErr
	}
	name := fmt.Sprintf("frame-%03d.jpg", index)
	path := filepath.Join(dir, name)
	_, err = command(
		ctx,
		"ffmpeg",
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
		fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", width, width),
		"-q:v",
		"3",
		path,
	)
	return Output{Name: name, MIME: "image/jpeg", Path: path}, err
}
