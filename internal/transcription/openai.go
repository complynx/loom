package transcription

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/textproto"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

const (
	MaxAudioBytes     int64 = 25_000_000
	MaxResponseBytes  int64 = 1_048_576
	defaultTimeout          = 2 * time.Minute
	maxRequestIDBytes       = 256
)

// Options configures a trusted provider endpoint. HTTP is restricted to loopback QA.
type Options struct {
	Endpoint   string
	APIKey     string
	Timeout    time.Duration
	HTTPClient *http.Client
}

// OpenAI streams multipart audio and makes exactly one HTTP attempt per call.
type OpenAI struct {
	endpoint string
	key      string
	timeout  time.Duration
	client   *http.Client
}

// NewOpenAI accepts a full /audio/transcriptions endpoint, not a model-controlled URL.
func NewOpenAI(options Options) (*OpenAI, error) {
	endpoint := options.Endpoint
	if endpoint == "" {
		endpoint = "https://api.openai.com/v1/audio/transcriptions"
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("invalid transcription endpoint")
	}
	address := net.ParseIP(parsed.Hostname())
	loopback := parsed.Hostname() == "localhost" || (address != nil && address.IsLoopback())
	if parsed.Scheme != "https" && (parsed.Scheme != "http" || !loopback) {
		return nil, errors.New("transcription endpoint requires HTTPS")
	}
	if strings.TrimSpace(options.APIKey) == "" || strings.ContainsAny(options.APIKey, "\r\n") {
		return nil, errors.New("invalid transcription credential")
	}
	timeout := options.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	if timeout < 0 {
		return nil, errors.New("invalid transcription timeout")
	}
	client := &http.Client{Timeout: timeout}
	if options.HTTPClient != nil {
		*client = *options.HTTPClient
	}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &OpenAI{endpoint: endpoint, key: options.APIKey, timeout: timeout, client: client}, nil
}

// Transcribe never retries a dispatched request, including 429 and uncertain network failures.
func (provider *OpenAI) Transcribe(ctx context.Context, input Request) (Result, error) {
	if err := validateRequest(ctx, input); err != nil {
		return Result{}, failure(FailurePreDispatch, err.Error())
	}
	body, contentType, err := multipartAudio(input)
	if err != nil {
		return Result{}, failure(FailurePreDispatch, "cannot prepare audio upload")
	}
	ctx, cancel := context.WithTimeout(ctx, provider.timeout)
	defer cancel()
	var dispatched atomic.Bool
	trace := &httptrace.ClientTrace{WroteHeaders: func() { dispatched.Store(true) }}
	request, err := http.NewRequestWithContext(
		httptrace.WithClientTrace(ctx, trace),
		http.MethodPost,
		provider.endpoint,
		body,
	)
	if err != nil {
		return Result{}, failure(FailurePreDispatch, "cannot prepare provider request")
	}
	request.Header.Set("Authorization", "Bearer "+provider.key)
	request.Header.Set("Content-Type", contentType)
	if input.ClientRequestID != "" {
		request.Header.Set("X-Client-Request-Id", input.ClientRequestID)
	}
	response, err := provider.client.Do(request)
	if err != nil {
		kind := FailurePreDispatch
		if dispatched.Load() {
			kind = FailureUnknown
		}
		return Result{}, failure(kind, "provider transport failed")
	}
	defer func() { _ = response.Body.Close() }()
	requestID := response.Header.Get("X-Request-ID")
	if len(requestID) > maxRequestIDBytes {
		requestID = ""
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		kind := FailureRejected
		// A server-side failure can occur after paid processing; keep the durable intent unresolved.
		if response.StatusCode >= http.StatusInternalServerError {
			kind = FailureUnknown
		}
		return Result{}, &FailureError{
			Kind:       kind,
			StatusCode: response.StatusCode,
			RequestID:  requestID,
			Reason:     "provider returned non-success status",
		}
	}
	result, err := decodeResult(response.Body, input.Model)
	if err != nil {
		return Result{}, &FailureError{
			Kind:      FailureUnknown,
			RequestID: requestID,
			Reason:    "invalid or oversized provider receipt",
		}
	}
	result.RequestID = requestID
	return result, nil
}

func failure(kind, reason string) *FailureError { return &FailureError{Kind: kind, Reason: reason} }

func validateRequest(ctx context.Context, input Request) error {
	if ctx.Err() != nil {
		return errors.New("transcription context cancelled before dispatch")
	}
	if input.Audio == nil || strings.TrimSpace(input.Model) == "" || strings.TrimSpace(input.Filename) == "" {
		return errors.New("transcription requires audio, model, and filename")
	}
	if len(input.Model) > 128 || len(input.Filename) > 256 || len(input.Prompt) > 8192 || len(input.Language) > 16 {
		return errors.New("transcription metadata exceeds limit")
	}
	if !finite(input.DurationSeconds) || input.DurationSeconds < 0 {
		return errors.New("invalid audio duration")
	}
	if strings.ContainsAny(input.Filename+input.MIME, "\r\n") {
		return errors.New("invalid audio metadata")
	}
	return nil
}

func multipartAudio(input Request) (io.Reader, string, error) {
	var prefix bytes.Buffer
	writer := multipart.NewWriter(&prefix)
	format := "json"
	if input.Model == "whisper-1" {
		format = "verbose_json"
	}
	fields := [][2]string{{"model", input.Model}, {"response_format", format}}
	if input.Language != "" {
		fields = append(fields, [2]string{"language", input.Language})
	}
	if input.Prompt != "" {
		fields = append(fields, [2]string{"prompt", input.Prompt})
	}
	for _, field := range fields {
		if err := writer.WriteField(field[0], field[1]); err != nil {
			return nil, "", err
		}
	}
	header := make(textproto.MIMEHeader)
	header.Set(
		"Content-Disposition",
		mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": filepath.Base(input.Filename)}),
	)
	contentType := input.MIME
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if _, _, err := mime.ParseMediaType(contentType); err != nil {
		return nil, "", err
	}
	header.Set("Content-Type", contentType)
	if _, err := writer.CreatePart(header); err != nil {
		return nil, "", err
	}
	offset := prefix.Len()
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	bytesValue := prefix.Bytes()
	return io.MultiReader(
		bytes.NewReader(bytesValue[:offset]),
		&boundedAudio{source: input.Audio, remaining: MaxAudioBytes},
		bytes.NewReader(bytesValue[offset:]),
	), writer.FormDataContentType(), nil
}

type boundedAudio struct {
	source    io.Reader
	remaining int64
}

func (reader *boundedAudio) Read(buffer []byte) (int, error) {
	if reader.remaining == 0 {
		var probe [1]byte
		count, err := reader.source.Read(probe[:])
		if count > 0 {
			return 0, errors.New("audio upload exceeds 25 MB")
		}
		return 0, err
	}
	if int64(len(buffer)) > reader.remaining {
		buffer = buffer[:reader.remaining]
	}
	count, err := reader.source.Read(buffer)
	reader.remaining -= int64(count)
	return count, err
}

func decodeResult(reader io.Reader, model string) (Result, error) {
	data, err := io.ReadAll(io.LimitReader(reader, MaxResponseBytes+1))
	if err != nil || int64(len(data)) > MaxResponseBytes {
		return Result{}, errors.New("invalid receipt body")
	}
	var receipt audioReceipt
	if decodeErr := json.Unmarshal(data, &receipt); decodeErr != nil || receipt.Text == nil {
		return Result{}, errors.New("invalid transcript JSON")
	}
	usage, usageErr := receipt.billingUsage(model)
	if usageErr != nil {
		return Result{}, usageErr
	}
	if _, costErr := Cost(usage, Tariff{DurationDenominator: 1}); costErr != nil {
		return Result{}, costErr
	}
	for _, segment := range receipt.Segments {
		if !finite(segment.Start) || !finite(segment.End) || segment.Start < 0 || segment.End < segment.Start {
			return Result{}, errors.New("invalid transcript segment")
		}
	}
	return Result{Text: *receipt.Text, Segments: receipt.Segments, Usage: usage}, nil
}

// Receipt pointers distinguish a reported zero from an absent billable unit.
type audioReceipt struct {
	Text     *string   `json:"text"`
	Segments []Segment `json:"segments"`
	Duration *float64  `json:"duration"`
	Usage    struct {
		Type         string   `json:"type"`
		InputTokens  *int64   `json:"input_tokens"`
		OutputTokens *int64   `json:"output_tokens"`
		Seconds      *float64 `json:"seconds"`
		Details      struct {
			AudioTokens *int64 `json:"audio_tokens"`
			TextTokens  *int64 `json:"text_tokens"`
		} `json:"input_token_details"`
	} `json:"usage"`
}

func (receipt audioReceipt) billingUsage(model string) (Usage, error) {
	switch receipt.Usage.Type {
	case UsageTokens:
		details := receipt.Usage.Details
		if receipt.Usage.InputTokens == nil || receipt.Usage.OutputTokens == nil || details.AudioTokens == nil ||
			details.TextTokens == nil {
			return Usage{}, errors.New("missing transcription token units")
		}
		return Usage{
			Type:         UsageTokens,
			InputTokens:  *receipt.Usage.InputTokens,
			OutputTokens: *receipt.Usage.OutputTokens,
			AudioTokens:  *details.AudioTokens,
			TextTokens:   *details.TextTokens,
		}, nil
	case UsageDuration:
		if receipt.Usage.Seconds == nil {
			return Usage{}, errors.New("missing transcription duration")
		}
		return Usage{Type: UsageDuration, DurationSeconds: *receipt.Usage.Seconds}, nil
	default:
		if receipt.Usage.Type == "" && model == "whisper-1" && receipt.Duration != nil {
			return Usage{Type: UsageDuration, DurationSeconds: *receipt.Duration}, nil
		}
		return Usage{}, errors.New("missing or unknown transcription usage")
	}
}
