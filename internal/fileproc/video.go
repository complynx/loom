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
}

func processVideo(ctx context.Context, req Request, input, dir string) (Result, error) {
	var p VideoOptions
	if err := decodeOptions(req.Params, &p); err != nil {
		return Result{}, err
	}
	var err error
	p, err = videoOptions(p)
	if err != nil {
		return Result{}, err
	}
	b, err := command(
		ctx,
		"ffprobe",
		"-v",
		"error",
		"-protocol_whitelist",
		"file,pipe",
		"-show_entries",
		"format=duration,format_name,size:stream=index,codec_name,codec_type,width,height,duration,sample_rate,channels",
		"-of",
		"json",
		input,
	)
	if err != nil {
		return Result{}, err
	}
	if req.Operation == "video.info" {
		if !json.Valid(b) {
			return Result{}, errors.New("invalid probe result")
		}
		return Result{Data: b}, nil
	}
	var probe struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType string `json:"codec_type"`
			Duration  string `json:"duration"`
		} `json:"streams"`
	}
	if err = json.Unmarshal(b, &probe); err != nil {
		return Result{}, err
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
	if req.Operation == "video.audio" {
		return videoAudio(ctx, p, input, dir, duration, hasAudio)
	}
	timestamps, partial, next, err := videoTimestamps(req, p, duration)
	if err != nil {
		return Result{}, err
	}
	return videoFrames(ctx, p, input, dir, duration, hasAudio, timestamps, partial, next)
}

func videoOptions(p VideoOptions) (VideoOptions, error) {
	if p.Width == 0 {
		p.Width = 1280
	}
	if p.MaxFrames == 0 {
		p.MaxFrames = 12
	}
	if p.Interval == 0 {
		p.Interval = 10
	}
	if p.Start < 0 || p.End < 0 || p.Timestamp < 0 || p.Width < 1 || p.Width > 2048 || p.MaxFrames < 1 ||
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
		for at := p.Start; at < end; at += p.Interval {
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
	timestamps []float64,
	partial bool,
	next float64,
) (Result, error) {
	result, err := dataResult(map[string]any{"timestamps": timestamps, hasAudioKey: hasAudio, "duration": duration})
	if err != nil {
		return Result{}, err
	}
	for index, at := range timestamps {
		if duration > 0 && at >= duration {
			return Result{}, errors.New("frame timestamp exceeds duration")
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
			"-ss",
			number(at),
			"-protocol_whitelist",
			"file,pipe",
			"-i",
			input,
			"-frames:v",
			"1",
			"-vf",
			fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", p.Width, p.Width),
			"-q:v",
			"3",
			path,
		)
		if err != nil {
			if len(result.Outputs) > 0 {
				result.Partial = true
				result.NextCursor = number(at)
				return result, nil
			}
			return Result{}, err
		}
		result.Outputs = append(result.Outputs, Output{Name: name, MIME: "image/jpeg", Path: path})
	}
	result.Partial = partial
	if partial {
		result.NextCursor = number(next)
	}
	return result, nil
}
func number(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }
