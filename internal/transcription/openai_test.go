package transcription_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/complynx/loom/internal/transcription"
)

func TestStreamedTranscription(t *testing.T) {
	t.Parallel()
	payload := bytes.Repeat([]byte("sound"), 8192)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer synthetic-only" {
			t.Error("missing provider authorization")
		}
		fields := readFields(t, request)
		if fields["file"] != string(payload) || fields["model"] != "gpt-transcribe" ||
			fields["response_format"] != "json" {
			t.Error("incorrect streamed multipart contents")
		}
		writer.Header().Set("X-Request-ID", "receipt-1")
		_, _ = io.WriteString(writer, `{"text":"hello","usage":{"type":"duration","seconds":12.5}}`)
	}))
	defer server.Close()
	provider, err := transcription.NewOpenAI(transcription.Options{Endpoint: server.URL, APIKey: "synthetic-only"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.Transcribe(
		t.Context(),
		transcription.Request{
			Model:    "gpt-transcribe",
			Filename: "audio.wav",
			MIME:     "audio/wav",
			Audio:    bytes.NewReader(payload),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "hello" || result.RequestID != "receipt-1" || result.Usage.DurationSeconds != 12.5 {
		t.Fatalf("incorrect result: %+v", result)
	}
	cost, err := transcription.Cost(result.Usage, transcription.Tariff{DurationNumerator: 3, DurationDenominator: 2})
	if err != nil || cost != 19 {
		t.Fatalf("incorrect rounded cost: %d, %v", cost, err)
	}
}

func TestProviderFailureDoesNotRetry(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		status int
		kind   string
	}{{"rate_limit", http.StatusTooManyRequests, transcription.FailureRejected}, {"server_error", http.StatusInternalServerError, transcription.FailureUnknown}, {"redirect", http.StatusTemporaryRedirect, transcription.FailureRejected}}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				writer.Header().Set("Location", "https://example.invalid/steal")
				writer.WriteHeader(test.status)
			}))
			defer server.Close()
			provider, err := transcription.NewOpenAI(
				transcription.Options{Endpoint: server.URL, APIKey: "synthetic-only"},
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = provider.Transcribe(
				t.Context(),
				transcription.Request{Model: "gpt-transcribe", Filename: "a.wav", Audio: strings.NewReader("audio")},
			)
			var failure *transcription.FailureError
			if !errors.As(err, &failure) || failure.Kind != test.kind || calls.Load() != 1 {
				t.Fatalf("incorrect failure or retry: %v calls=%d", err, calls.Load())
			}
		})
	}
}

func TestCancelledBeforeDispatch(t *testing.T) {
	t.Parallel()
	provider, err := transcription.NewOpenAI(transcription.Options{APIKey: "synthetic-only"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = provider.Transcribe(
		ctx,
		transcription.Request{Model: "gpt-transcribe", Filename: "a.wav", Audio: strings.NewReader("audio")},
	)
	var failure *transcription.FailureError
	if !errors.As(err, &failure) || failure.Kind != transcription.FailurePreDispatch {
		t.Fatalf("incorrect cancellation: %v", err)
	}
}

func TestInvalidReceiptNeedsReconciliation(t *testing.T) {
	t.Parallel()
	receipts := []string{
		`{"text":"hello"}`,
		`{"text":"hello","usage":{"type":"tokens","input_tokens":10,"input_token_details":{"audio_tokens":11},"output_tokens":1}}`,
		strings.Repeat(" ", int(transcription.MaxResponseBytes)+1),
	}
	for _, receipt := range receipts {
		t.Run("receipt", func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(
				http.HandlerFunc(
					func(writer http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(writer, receipt) },
				),
			)
			defer server.Close()
			provider, err := transcription.NewOpenAI(
				transcription.Options{Endpoint: server.URL, APIKey: "synthetic-only"},
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = provider.Transcribe(
				t.Context(),
				transcription.Request{Model: "gpt-transcribe", Filename: "a.wav", Audio: strings.NewReader("audio")},
			)
			var failure *transcription.FailureError
			if !errors.As(err, &failure) || failure.Kind != transcription.FailureUnknown {
				t.Fatalf("incorrect receipt handling: %v", err)
			}
		})
	}
}

func TestCostUnitsAndOverflow(t *testing.T) {
	t.Parallel()
	tokens := transcription.Usage{Type: "tokens", InputTokens: 10, AudioTokens: 7, TextTokens: 3, OutputTokens: 4}
	cost, err := transcription.Cost(
		tokens,
		transcription.Tariff{AudioPerMillion: 1_000_000, TextPerMillion: 2_000_000, OutputPerMillion: 3_000_000},
	)
	if err != nil || cost != 25 {
		t.Fatalf("incorrect token cost %d %v", cost, err)
	}
	cost, err = transcription.Cost(
		transcription.Usage{Type: "duration", DurationSeconds: 0.1},
		transcription.Tariff{DurationNumerator: 10, DurationDenominator: 1},
	)
	if err != nil || cost != 1 {
		t.Fatalf("decimal duration rounded incorrectly: %d %v", cost, err)
	}
	_, err = transcription.Cost(transcription.Usage{}, transcription.Tariff{})
	if err == nil {
		t.Fatal("unknown usage accepted")
	}
	_, err = transcription.Cost(
		transcription.Usage{Type: "tokens", InputTokens: 1 << 62, AudioTokens: 1 << 62},
		transcription.Tariff{AudioPerMillion: 1 << 62},
	)
	if err == nil {
		t.Fatal("credit overflow accepted")
	}
}

func readFields(t *testing.T, request *http.Request) map[string]string {
	t.Helper()
	parts, err := request.MultipartReader()
	if err != nil {
		t.Error(err)
		return nil
	}
	fields := make(map[string]string)
	for {
		part, partErr := parts.NextPart()
		if errors.Is(partErr, io.EOF) {
			return fields
		}
		if partErr != nil {
			t.Error(partErr)
			return nil
		}
		value, readErr := io.ReadAll(part)
		if readErr != nil {
			t.Error(readErr)
			return nil
		}
		fields[part.FormName()] = string(value)
	}
}

func TestWhisperSegmentsAndTokenReceipt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, model, format, receipt string
		cost                         int64
		tariff                       transcription.Tariff
	}{
		{
			"whisper",
			"whisper-1",
			"verbose_json",
			`{"text":"hello","duration":2.5,"segments":[{"start":0,"end":2.5,"text":"hello"}]}`,
			3,
			transcription.Tariff{DurationNumerator: 1, DurationDenominator: 1},
		},
		{
			"tokens",
			"gpt-4o-mini-transcribe",
			"json",
			`{"text":"hello","usage":{"type":"tokens","input_tokens":10,"output_tokens":4,"input_token_details":{"audio_tokens":7,"text_tokens":3}}}`,
			25,
			transcription.Tariff{AudioPerMillion: 1_000_000, TextPerMillion: 2_000_000, OutputPerMillion: 3_000_000},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				fields := readFields(t, request)
				if fields["response_format"] != test.format {
					t.Error("incorrect response format")
				}
				_, _ = io.WriteString(writer, test.receipt)
			}))
			defer server.Close()
			provider, err := transcription.NewOpenAI(
				transcription.Options{Endpoint: server.URL, APIKey: "synthetic-only"},
			)
			if err != nil {
				t.Fatal(err)
			}
			result, err := provider.Transcribe(
				t.Context(),
				transcription.Request{Model: test.model, Filename: "a.wav", Audio: strings.NewReader("audio")},
			)
			if err != nil {
				t.Fatal(err)
			}
			cost, err := transcription.Cost(result.Usage, test.tariff)
			if err != nil || cost != test.cost {
				t.Fatalf("incorrect cost %d %v", cost, err)
			}
			if test.name == "whisper" && len(result.Segments) != 1 {
				t.Fatal("missing supported timestamps")
			}
		})
	}
}

func TestLostNetworkReceiptDoesNotRetry(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		hijacker, ok := writer.(http.Hijacker)
		if !ok {
			t.Error("server cannot hijack")
			return
		}
		connection, _, err := hijacker.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = connection.Close()
	}))
	defer server.Close()
	provider, err := transcription.NewOpenAI(transcription.Options{Endpoint: server.URL, APIKey: "synthetic-only"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Transcribe(
		t.Context(),
		transcription.Request{Model: "gpt-transcribe", Filename: "a.wav", Audio: strings.NewReader("audio")},
	)
	var failure *transcription.FailureError
	if !errors.As(err, &failure) || failure.Kind != transcription.FailureUnknown || calls.Load() != 1 {
		t.Fatalf("incorrect lost receipt handling: %v calls=%d", err, calls.Load())
	}
}

func TestOversizedAudioIsStopped(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, err := io.Copy(io.Discard, request.Body)
		if err == nil {
			t.Error("oversized upload was accepted")
		}
		writer.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	provider, err := transcription.NewOpenAI(transcription.Options{Endpoint: server.URL, APIKey: "synthetic-only"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Transcribe(
		t.Context(),
		transcription.Request{
			Model:    "gpt-transcribe",
			Filename: "a.wav",
			Audio:    io.LimitReader(zeroReader{}, transcription.MaxAudioBytes+1),
		},
	)
	if err == nil {
		t.Fatal("oversized audio produced successful receipt")
	}
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) { clear(buffer); return len(buffer), nil }
