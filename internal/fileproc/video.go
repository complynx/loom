package fileproc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
)

type VideoOptions struct {
	Start     float64 `json:"start,omitempty"`
	End       float64 `json:"end,omitempty"`
	Timestamp float64 `json:"timestamp,omitempty"`
	Interval  float64 `json:"interval,omitempty"`
	Width     int     `json:"width,omitempty"`
	MaxFrames int     `json:"max_frames,omitempty"`
	Quality   int     `json:"quality,omitempty"`
	Lossless  bool    `json:"lossless,omitempty"`
}

func processVideo(ctx context.Context, req Request, input, dir string) (Result, error) {
	if result, handled, err := processNativeAnimation(ctx, req, input, dir); handled || err != nil {
		return result, err
	}
	if result, handled, err := processAnimatedWebP(ctx, req, input, dir); handled || err != nil {
		return result, err
	}
	var p VideoOptions
	if err := decodeOptions(req.Params, &p); err != nil {
		return Result{}, err
	}
	requestedInterval := p.Interval
	var err error
	p, err = videoOptions(p)
	if err != nil {
		return Result{}, err
	}
	probe, err := probeVideo(ctx, input)
	if err != nil {
		return Result{}, err
	}
	if req.Operation == videoInfoOperation {
		data, marshalErr := json.Marshal(probe)
		return Result{Data: data}, marshalErr
	}
	duration, _ := strconv.ParseFloat(probe.Format.Duration, 64)
	hasAudio := false
	for _, s := range probe.Streams {
		if s.CodecType == "audio" {
			hasAudio = true
		}
		if duration <= 0 {
			duration, _ = strconv.ParseFloat(s.Duration, 64)
		}
	}
	if requestedInterval == 0 && duration > 0 && (probe.isAnimation() || duration < p.Interval) {
		p.Interval = math.Max(minAnimationInterval, duration/float64(p.MaxFrames))
	}
	if req.Operation == videoAudioOperation {
		return videoAudio(ctx, p, input, dir, duration, hasAudio)
	}
	selected, err := probe.visualStream()
	if err != nil {
		return Result{}, err
	}
	timestamps, partial, next, err := videoTimestamps(req, p, duration)
	if err != nil {
		return Result{}, err
	}
	stream := -1 // Preserve FFmpeg's automatic stream selection for ordinary video.
	if len(probe.FrameTimes) > 0 {
		stream = probe.Streams[selected].Index
	}
	return videoFrames(
		ctx,
		p,
		input,
		dir,
		duration,
		hasAudio,
		probe,
		stream,
		probe.FrameTimes,
		timestamps,
		partial,
		next,
	)
}

func videoOptions(p VideoOptions) (VideoOptions, error) {
	if p.Quality == 0 {
		p.Quality = 90
	}
	if p.Width == 0 {
		p.Width = 1280
	}
	if p.MaxFrames == 0 {
		p.MaxFrames = 12
	}
	if p.Interval == 0 {
		p.Interval = 10
	}
	if p.Quality < 1 || p.Quality > 100 || p.Start < 0 || p.End < 0 || p.Timestamp < 0 || p.Width < 1 ||
		p.Width > 2048 ||
		p.MaxFrames < 1 ||
		p.MaxFrames > MaxFrames ||
		p.Interval < 0.1 ||
		p.Interval > 3600 ||
		p.End > 0 && p.End <= p.Start {
		return p, errors.New("invalid video options")
	}
	for _, v := range []float64{p.Start, p.End, p.Timestamp, p.Interval} {
		if math.IsInf(v, 0) || math.IsNaN(v) {
			return p, errors.New("invalid timestamp")
		}
	}
	return p, nil
}

func videoAudio(
	ctx context.Context,
	p VideoOptions,
	input, dir string,
	duration float64,
	hasAudio bool,
) (Result, error) {
	if !hasAudio {
		return dataResult(map[string]any{hasAudioKey: false})
	}
	end := p.End
	if end == 0 {
		end = math.Min(p.Start+MaxAudioSeconds, duration)
	}
	if end <= p.Start || end-p.Start > 300 {
		return Result{}, errors.New("audio chunk must fit 300 seconds")
	}
	path := filepath.Join(dir, "audio.wav")
	_, err := command(
		ctx,
		"ffmpeg",
		"-v",
		"error",
		"-nostdin",
		"-y",
		"-ss",
		number(p.Start),
		"-protocol_whitelist",
		"file,pipe",
		"-i",
		input,
		"-t",
		number(end-p.Start),
		"-vn",
		"-ac",
		"1",
		"-ar",
		"16000",
		"-c:a",
		"pcm_s16le",
		path,
	)
	if err != nil {
		return Result{}, err
	}
	r, e := dataResult(map[string]any{hasAudioKey: true, "start": p.Start, "end": end})
	r.Outputs = []Output{{Name: "audio.wav", MIME: "audio/wav", Path: path}}
	r.Partial = duration > end
	if r.Partial {
		r.NextCursor = number(end)
	}
	return r, e
}
func videoTimestamps(req Request, p VideoOptions, duration float64) ([]float64, bool, float64, error) {
	timestamps := []float64{p.Timestamp}
	partial := false
	next := 0.0
	if req.Operation == "video.storyboard" {
		end := p.End
		if end == 0 {
			end = duration
		}
		if end <= p.Start {
			return nil, false, 0, errors.New("video duration or end is required")
		}
		timestamps = nil
		for index := 0; ; index++ {
			at := p.Start + float64(index)*p.Interval
			if at >= end || end-at < timestampTolerance {
				break
			}
			if len(timestamps) == p.MaxFrames {
				partial = true
				next = at
				break
			}
			timestamps = append(timestamps, at)
		}
	}
	return timestamps, partial, next, nil
}

func videoFrames(
	ctx context.Context,
	p VideoOptions,
	input, dir string,
	duration float64,
	hasAudio bool,
	probe videoProbe,
	stream int,
	frameTimes []float64,
	timestamps []float64,
	partial bool,
	next float64,
) (Result, error) {
	result, err := dataResult(map[string]any{timestampsKey: timestamps, hasAudioKey: hasAudio, durationKey: duration})
	if err != nil {
		return Result{}, err
	}
	for index, at := range timestamps {
		if duration > 0 && at >= duration {
			return Result{}, errors.New("frame timestamp exceeds duration")
		}
		name := fmt.Sprintf("frame-%03d.webp", index)
		path := filepath.Join(dir, name)
		args := []string{
			"-v",
			"error",
			"-nostdin",
			"-y",
		}
		if len(frameTimes) == 0 {
			args = append(args, "-ss", number(at))
		}
		args = append(args, probe.decoderOptions()...)
		args = append(args, "-protocol_whitelist", "file,pipe", "-i", input)
		args = append(args, videoFrameFilterOptions(p.Width, at, frameTimes, stream, probe)...)
		args = append(args,
			"-frames:v",
			"1",
			"-threads",
			"1",
		)
		args = append(args, webPEncodingOptions(p)...)
		args = append(args, "-update", "1", path)
		_, err = command(ctx, "ffmpeg", args...)
		if err == nil {
			err = validateFrame(path)
		}
		if err != nil {
			if len(result.Outputs) > 0 {
				result.Partial = true
				result.NextCursor = preciseSeconds(at)
				return result, nil
			}
			return Result{}, err
		}
		result.Outputs = append(result.Outputs, Output{Name: name, MIME: webPMIME, Path: path})
	}
	result.Partial = partial
	if partial {
		result.NextCursor = preciseSeconds(next)
	}
	return result, nil
}

func videoFrameFilter(width int, at float64, times []float64) string {
	filter := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", width, width)
	if len(times) == 0 {
		return filter
	}
	frame := 0
	for i, timestamp := range times {
		if timestamp > at {
			break
		}
		frame = i
	}
	return fmt.Sprintf("select=eq(n\\,%d),%s", frame, filter)
}
func number(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }

func preciseSeconds(v float64) string {
	short := number(v)
	parsed, _ := strconv.ParseFloat(short, 64)
	if parsed == v {
		return short
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func videoFrameFilterOptions(width int, at float64, frameTimes []float64, stream int, probe videoProbe) []string {
	filter := videoFrameFilter(width, at, frameTimes)
	if probe.AlphaStream >= 0 {
		return []string{"-filter_complex", fmt.Sprintf("[0:%d][0:%d]alphamerge,%s[out]",
			stream, probe.Streams[probe.AlphaStream].Index, filter), "-map", "[out]"}
	}
	args := []string{"-vf", filter}
	if stream >= 0 {
		args = append(args, "-map", fmt.Sprintf("0:%d", stream))
	}
	return args
}
