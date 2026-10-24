package fileproc

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
)

const (
	maxAnimationFrames  = 1000
	animationSniffBytes = 512
	maxAnimationPixels  = 16_000_000
	lottieKind          = "lottie"
	timestampsKey       = "timestamps"
	pngMIME             = "image/png"
	videoCodecType      = "video"
	lottieImageLayer    = 2
	lottieTextLayer     = 5
)

type animationInfo struct {
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Frames   int     `json:"frames"`
	Duration float64 `json:"duration"`
}

type lottieHeader struct {
	Width  int               `json:"w"`
	Height int               `json:"h"`
	Rate   float64           `json:"fr"`
	Start  float64           `json:"ip"`
	End    float64           `json:"op"`
	Layers []json.RawMessage `json:"layers"`
}

// Native animation renderers receive only staged bytes and worker-owned paths.
func processNativeAnimation(ctx context.Context, req Request, input, dir string) (Result, bool, error) {
	f, err := os.Open(input) // #nosec G703 -- input is the worker-owned staged file.
	if err != nil {
		return Result{}, false, err
	}
	header := make([]byte, animationSniffBytes)
	n, readErr := f.Read(header)
	_ = f.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return Result{}, false, readErr
	}
	header = header[:n]
	kind := ""
	if bytes.HasPrefix(header, []byte("\x8aMNG\r\n\x1a\n")) {
		kind = "mng"
	}
	trimmed := bytes.TrimSpace(header)
	if bytes.HasPrefix(header, []byte{0x1f, 0x8b}) || bytes.HasPrefix(trimmed, []byte("{")) {
		kind = lottieKind
	}
	if kind == "" {
		return Result{}, false, nil
	}
	if kind == lottieKind {
		var prepareErr error
		input, prepareErr = prepareLottie(ctx, input, dir)
		if prepareErr != nil {
			return Result{}, true, prepareErr
		}
	}
	r, err := nativeAnimationResult(ctx, req, kind, input, dir)
	return r, true, err
}

func prepareLottie(ctx context.Context, input, dir string) (string, error) {
	file, err := os.Open(input) // #nosec G703 -- input is the worker-owned staged file.
	if err != nil {
		return "", err
	}
	defer file.Close()
	var reader io.Reader = file
	var signature [2]byte
	if _, err = io.ReadFull(file, signature[:]); err != nil {
		return "", err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	if signature == [2]byte{0x1f, 0x8b} {
		compressed, zipErr := gzip.NewReader(file)
		if zipErr != nil {
			return "", zipErr
		}
		defer compressed.Close()
		reader = compressed
	}
	data, err := io.ReadAll(io.LimitReader(reader, MaxInputBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > MaxInputBytes {
		return "", errors.New("expanded TGS exceeds input limit")
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	var value any
	if err = json.Unmarshal(data, &value); err != nil {
		return "", err
	}
	var info lottieHeader
	if err = json.Unmarshal(data, &info); err != nil {
		return "", err
	}
	if info.Width < 1 || info.Height < 1 || info.Width > maxAnimationPixels/info.Height || info.Rate <= 0 ||
		info.End <= info.Start ||
		info.End-info.Start > maxAnimationFrames ||
		len(info.Layers) == 0 {
		return "", errors.New("invalid or oversized Lottie animation")
	}
	if err = validateLottie(value); err != nil {
		return "", err
	}
	// Preserve property order for rlottie, but reject ambiguous duplicate keys.
	if err = uniqueJSONKeys(json.NewDecoder(bytes.NewReader(data))); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "animation.json")
	return path, os.WriteFile(path, data, 0600) // #nosec G703 -- dir is the worker-created job directory.
}

func uniqueJSONKeys(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		if err = uniqueJSONObjectKeys(decoder); err != nil {
			return err
		}
	case '[':
		for decoder.More() {
			if err = uniqueJSONKeys(decoder); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	_, err = decoder.Token()
	return err
}

func uniqueJSONObjectKeys(decoder *json.Decoder) error {
	seen := map[string]struct{}{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return errors.New("JSON object key must be a string")
		}
		if _, duplicate := seen[key]; duplicate {
			return errors.New("duplicate JSON key in Lottie animation")
		}
		seen[key] = struct{}{}
		if err = uniqueJSONKeys(decoder); err != nil {
			return err
		}
	}
	return nil
}

// Telegram TGS uses self-contained shapes. Fail closed on external assets or expressions.
func validateLottie(value any) error {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if err := validateLottieProperty(key, child); err != nil {
				return err
			}
			if err := validateLottie(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range v {
			if err := validateLottie(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateLottieProperty(key string, value any) error {
	if text, ok := value.(string); ok && text != "" && (key == "p" || key == "u" || key == "x" || key == "fPath") {
		return errors.New("lottie external resources and expressions are unsupported")
	}
	if kind, ok := value.(float64); key == "ty" && ok && (kind == lottieImageLayer || kind == lottieTextLayer) {
		return errors.New("lottie image and text layers are unsupported")
	}
	return nil
}

func nativeAnimationResult(ctx context.Context, req Request, kind, input, dir string) (Result, error) {
	var raw VideoOptions
	if err := decodeOptions(req.Params, &raw); err != nil {
		return Result{}, err
	}
	p, err := videoOptions(raw)
	if err != nil {
		return Result{}, err
	}
	info, err := readAnimationInfo(ctx, kind, input)
	if err != nil {
		return Result{}, err
	}
	if info.Width < 1 || info.Height < 1 || info.Width > maxAnimationPixels/info.Height || info.Frames < 1 ||
		info.Frames > maxAnimationFrames ||
		info.Duration <= 0 ||
		math.IsNaN(info.Duration) ||
		math.IsInf(info.Duration, 0) {
		return Result{}, errors.New("invalid animation metadata")
	}
	if req.Operation == videoInfoOperation {
		return animationMetadata(kind, info)
	}
	if req.Operation == videoAudioOperation {
		return dataResult(map[string]any{hasAudioKey: false})
	}
	if raw.Interval == 0 {
		p.Interval = math.Max(minAnimationInterval, info.Duration/float64(p.MaxFrames))
	}
	times, partial, next, err := videoTimestamps(req, p, info.Duration)
	if err != nil {
		return Result{}, err
	}
	r, err := dataResult(map[string]any{timestampsKey: times, durationKey: info.Duration, hasAudioKey: false})
	if err != nil {
		return Result{}, err
	}
	for index, at := range times {
		if at >= info.Duration {
			return Result{}, errors.New("frame timestamp exceeds duration")
		}
		name := fmt.Sprintf("frame-%03d.png", index)
		path := filepath.Join(dir, name)
		if _, err = command(
			ctx,
			"loom-animation",
			kind,
			input,
			path,
			strconv.FormatFloat(at, 'f', -1, 64),
			strconv.Itoa(p.Width),
		); err != nil {
			return Result{}, err
		}
		r.Outputs = append(r.Outputs, Output{Name: name, MIME: pngMIME, Path: path})
	}
	r.Partial = partial
	if partial {
		r.NextCursor = preciseSeconds(next)
	}
	return r, nil
}

func readAnimationInfo(ctx context.Context, kind, input string) (animationInfo, error) {
	var info animationInfo
	if kind == lottieKind {
		data, err := os.ReadFile(input) // #nosec G703 -- input is the validated, worker-written JSON file.
		if err != nil {
			return info, err
		}
		var header lottieHeader
		if err = json.Unmarshal(data, &header); err != nil {
			return info, err
		}
		// Lottie's out point is exclusive; some native APIs include an extra terminal frame.
		return animationInfo{
			Width:    header.Width,
			Height:   header.Height,
			Frames:   int(math.Ceil(header.End - header.Start)),
			Duration: (header.End - header.Start) / header.Rate,
		}, nil
	}
	metadata, err := command(ctx, "loom-animation", kind, input)
	if err != nil {
		return info, err
	}
	err = json.Unmarshal(metadata, &info)
	return info, err
}

func animationMetadata(kind string, info animationInfo) (Result, error) {
	return dataResult(
		map[string]any{
			"format": map[string]any{"format_name": kind, durationKey: preciseSeconds(info.Duration)},
			"streams": []any{
				map[string]any{
					"index":      0,
					"codec_type": videoCodecType,
					"codec_name": kind,
					"width":      info.Width,
					"height":     info.Height,
					"nb_frames":  strconv.Itoa(info.Frames),
					durationKey:  preciseSeconds(info.Duration),
				},
			},
		},
	)
}
