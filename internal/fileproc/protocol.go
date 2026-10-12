package fileproc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"
)

const (
	MaxInputBytes      int64 = 20 << 20
	MaxOutputBytes     int64 = 64 << 20
	MaxTextBytes             = 64 << 10
	MaxResultJSONBytes       = 256 << 10
	MaxFrames                = 24
)

const (
	DefaultTimeout   = 30 * time.Second
	MaxJobBytes      = 16 << 10
	MaxStderrBytes   = 8192
	MaxSearchLine    = 1024
	MaxSearchMatches = 100
	MaxAudioSeconds  = 300
	hasAudioKey      = "has_audio"
)

type Request struct {
	Operation string          `json:"operation"`
	Params    json.RawMessage `json:"params"`
}
type Output struct {
	Name string `json:"name"`
	MIME string `json:"mime"`
	Path string `json:"-"`
}
type Result struct {
	Data       json.RawMessage `json:"data"`
	Outputs    []Output        `json:"outputs,omitempty"`
	Partial    bool            `json:"partial"`
	NextCursor string          `json:"next_cursor,omitempty"`
}
type LocalProcessor func(context.Context, Request, string, string) (Result, error)
type Limits struct {
	InputBytes  int64
	OutputBytes int64
	Timeout     time.Duration
	Concurrency int
}

func (l Limits) defaults() Limits {
	if l.InputBytes == 0 {
		l.InputBytes = MaxInputBytes
	}
	if l.OutputBytes == 0 {
		l.OutputBytes = MaxOutputBytes
	}
	if l.Timeout == 0 {
		l.Timeout = DefaultTimeout
	}
	if l.Concurrency == 0 {
		l.Concurrency = 2
	}
	return l
}
func (l Limits) Validate() error {
	l = l.defaults()
	if l.InputBytes < 1 || l.InputBytes > MaxInputBytes || l.OutputBytes < 1 || l.OutputBytes > MaxOutputBytes ||
		l.Timeout < time.Millisecond ||
		l.Timeout > DefaultTimeout ||
		l.Concurrency < 1 ||
		l.Concurrency > 2 {
		return errors.New("invalid file processor limits")
	}
	return nil
}
func dataResult(value any) (Result, error) {
	b, err := json.Marshal(value)
	return Result{Data: b}, err
}
func decodeOptions(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	dec := json.NewDecoder(bytesReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("unexpected trailing JSON")
	}
	return nil
}
func Process(ctx context.Context, req Request, inputPath, outputDir string) (Result, error) {
	switch req.Operation {
	case "pdf.info", "pdf.text", "pdf.search", "pdf.render", "pdf.images":
		return processPDF(ctx, req, inputPath, outputDir)
	case "video.info", "video.storyboard", "video.frame", "video.audio":
		return processVideo(ctx, req, inputPath, outputDir)
	default:
		return Result{}, errors.New("unsupported file operation")
	}
}
