package fileproc

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strconv"
	"strings"
)

type videoProbe struct {
	ImageSequence bool      `json:"-"`
	FrameTimes    []float64 `json:"-"`
	Format        struct {
		Duration string `json:"duration,omitempty"`
		Name     string `json:"format_name"`
		Size     string `json:"size,omitempty"`
	} `json:"format"`
	Streams []videoStream `json:"streams"`
}

type videoStream struct {
	Index       int    `json:"index"`
	CodecName   string `json:"codec_name"`
	CodecType   string `json:"codec_type"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	Duration    string `json:"duration,omitempty"`
	Frames      string `json:"nb_frames,omitempty"`
	SampleRate  string `json:"sample_rate,omitempty"`
	Channels    int    `json:"channels,omitempty"`
	AverageRate string `json:"avg_frame_rate,omitempty"`
}

func probeVideo(ctx context.Context, input string) (videoProbe, error) {
	var probe videoProbe
	b, err := command(
		ctx,
		"ffprobe",
		"-v",
		"error",
		"-protocol_whitelist",
		"file,pipe",
		"-show_entries",
		"format=duration,format_name,size:stream=index,codec_name,codec_type,width,height,duration,nb_frames,sample_rate,channels,avg_frame_rate",
		"-of",
		"json",
		input,
	)
	if err != nil {
		return probe, err
	}
	if err = json.Unmarshal(b, &probe); err != nil {
		return probe, err
	}
	probe.ImageSequence = isImageSequence(input)
	selected, err := probe.visualStream()
	if errors.Is(err, errNoVideo) {
		return probe, nil
	} // Audio-only inputs remain valid for metadata/audio tools.
	if err != nil {
		return probe, err
	}
	if probe.isAnimation() || probe.shortSilentWebM() {
		duration, times, frameErr := probeFrameTimeline(ctx, input, probe.Streams[selected].Index)
		if frameErr != nil {
			if probe.isAnimation() || ctx.Err() != nil {
				return probe, frameErr
			}
			return probe, nil // The optional silent-video timeline must not reject ordinary videos.
		}
		probe.FrameTimes = times
		probe.Streams[selected].Duration = preciseSeconds(duration)
		probe.Format.Duration = preciseSeconds(duration)
	}
	return probe, nil
}

func (p videoProbe) shortSilentWebM() bool {
	const maxClipSeconds = 30
	if p.Format.Name != "matroska,webm" || positiveSeconds(p.Format.Duration) == 0 ||
		positiveSeconds(p.Format.Duration) > maxClipSeconds {
		return false
	}
	visual := false
	for _, s := range p.Streams {
		if s.CodecType == "audio" {
			return false
		}
		if s.CodecType == videoCodecType {
			frames, _ := strconv.Atoi(s.Frames)
			numerator, denominator, ok := strings.Cut(s.AverageRate, "/")
			rate := 0.0
			if ok && positiveSeconds(denominator) > 0 {
				rate = positiveSeconds(numerator) / positiveSeconds(denominator)
			}
			if frames > maxAnimationFrames || rate <= 0 ||
				positiveSeconds(p.Format.Duration)*rate > maxAnimationFrames {
				return false
			}
			visual = true
		}
	}
	return visual
}

func (p videoProbe) visualStream() (int, error) {
	selected := -1
	for i, s := range p.Streams {
		if s.CodecType != videoCodecType {
			continue
		}
		if !p.ImageSequence {
			return i, nil
		}
		frames, _ := strconv.Atoi(s.Frames)
		if selected < 0 {
			selected = i
			continue
		}
		previous, _ := strconv.Atoi(p.Streams[selected].Frames)
		// AVIF/HEIF may put a still cover before the timed image sequence.
		if frames > previous ||
			frames == previous && positiveSeconds(s.Duration) > positiveSeconds(p.Streams[selected].Duration) {
			selected = i
		}
	}
	if selected < 0 {
		return 0, errNoVideo
	}
	return selected, nil
}

func (p videoProbe) isAnimation() bool {
	if p.Format.Name == "gif" || p.Format.Name == "apng" || p.Format.Name == "jpegxl_anim" {
		return true
	}
	// Timed image tracks are marked by the container brands; codec alone cannot distinguish them from video.
	return p.ImageSequence
}

func isImageSequence(input string) bool {
	f, err := os.Open(input) // #nosec G703 -- input is the worker-owned staged file.
	if err != nil {
		return false
	}
	defer f.Close()
	var header [12]byte
	if _, err = f.Read(header[:]); err != nil || string(header[4:8]) != "ftyp" {
		return false
	}
	switch string(header[8:]) {
	case "avif", "avis", "heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1":
		return true
	default:
		return false
	}
}

var errNoVideo = errors.New("video stream is required")

func validateFrame(path string) error {
	stat, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !stat.Mode().IsRegular() || stat.Size() == 0 {
		return errors.New("decoder produced no frame")
	}
	return nil
}

func positiveSeconds(text string) float64 {
	v, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
		return 0
	}
	return v
}

// Frame timestamps recover APNG/JXL durations which their demuxers omit from stream metadata.
func probeFrameTimeline(ctx context.Context, input string, stream int) (float64, []float64, error) {
	b, err := command(
		ctx,
		"ffprobe",
		"-v",
		"error",
		"-protocol_whitelist",
		"file,pipe",
		"-select_streams",
		strconv.Itoa(stream),
		"-show_entries",
		"frame=best_effort_timestamp_time,duration_time",
		"-of",
		"json",
		input,
	)
	if err != nil {
		return 0, nil, err
	}
	var result struct {
		Frames []struct {
			At       string `json:"best_effort_timestamp_time"`
			Duration string `json:"duration_time"`
		} `json:"frames"`
	}
	if err = json.Unmarshal(b, &result); err != nil {
		return 0, nil, err
	}
	if len(result.Frames) == 0 || len(result.Frames) > maxAnimationFrames {
		return 0, nil, errors.New("animation frame count exceeds bounds")
	}
	duration := 0.0
	times := make([]float64, 0, len(result.Frames))
	for _, f := range result.Frames {
		at, parseErr := strconv.ParseFloat(f.At, 64)
		if parseErr != nil || math.IsNaN(at) || math.IsInf(at, 0) || at < 0 ||
			len(times) > 0 && at < times[len(times)-1] {
			return 0, nil, errors.New("invalid animation timestamp")
		}
		times = append(times, at)
		duration = math.Max(duration, at+positiveSeconds(f.Duration))
	}
	if duration <= 0 {
		return 0, nil, errors.New("animation has no timed frames")
	}
	return duration, times, nil
}
