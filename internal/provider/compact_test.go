package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/complynx/loom/internal/provider"
)

const compactReply = `{"id":"compact_1","object":"response.compaction","output":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Find the order"}]},{"type":"compaction","id":"cmp_1","encrypted_content":"opaque-state"}],"usage":{"input_tokens":200,"output_tokens":30,"input_tokens_details":{"cached_tokens":120}}}`

func TestCompactPreservesCanonicalWindowAndUsage(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/responses/compact" ||
			r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("X-Client-Request-Id") != "stable-id" {
			t.Error("wrong compact dispatch")
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if string(body["model"]) != `"test"` || string(body["instructions"]) != `"Keep facts"` ||
			string(body["service_tier"]) != `"default"` || len(body) != 4 {
			t.Errorf("compact parameters: %v", body)
		}
		_, _ = w.Write([]byte(compactReply))
	}))
	defer server.Close()
	client, err := provider.NewResponses(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	out, err := client.Compact(context.Background(), provider.Request{
		Provider: "openai", Model: "test", Instructions: "Keep facts", ClientRequestID: "stable-id",
		Input: []provider.Item{provider.Message("user", "Find the order")}, Effort: "medium", MaxOutputTokens: 100,
		Tools: []provider.Tool{{Type: "function", Name: "must-not-dispatch"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ID != "compact_1" || len(out.Output) != 2 || out.Usage.InputTokens != 200 ||
		out.Usage.OutputTokens != 30 || out.Usage.CachedInputTokens != 120 || len(out.Calls) != 0 || out.Text != "" {
		t.Fatalf("lost compact context or accounting: %+v", out)
	}
	if string(out.Output[1]) != `{"type":"compaction","id":"cmp_1","encrypted_content":"opaque-state"}` {
		t.Fatalf("opaque item changed: %s", out.Output[1])
	}
}

func TestCompactFailuresKeepBillingCertainty(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		status  int
		body    string
		unknown bool
		input   int64
	}{
		{name: "rejected", status: http.StatusBadRequest, body: `{}`, unknown: false},
		{name: "server failure", status: http.StatusInternalServerError, body: `{}`, unknown: true},
		{name: "null reply", status: http.StatusOK, body: `null`, unknown: true},
		{
			name:    "missing usage",
			status:  http.StatusOK,
			body:    `{"id":"x","object":"response.compaction","output":[]}`,
			unknown: true,
		},
		{
			name:    "invalid usage",
			status:  http.StatusOK,
			body:    `{"id":"x","object":"response.compaction","output":[],"usage":{"input_tokens":1,"output_tokens":2,"input_tokens_details":{"cached_tokens":3}}}`,
			unknown: true,
		},
		{
			name:   "missing state",
			status: http.StatusOK,
			body:   `{"id":"x","object":"response.compaction","output":[],"usage":{"input_tokens":7,"output_tokens":2}}`,
			input:  7,
		},
		{
			name:   "tool output",
			status: http.StatusOK,
			body:   `{"id":"x","object":"response.compaction","output":[{"type":"function_call","call_id":"c","name":"script","arguments":"{}"}],"usage":{"input_tokens":7,"output_tokens":2}}`,
			input:  7,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			client, err := provider.NewResponses(server.URL, "secret", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			out, err := client.Compact(context.Background(), provider.Request{
				Model: "test", Input: []provider.Item{provider.Message("user", "hello")},
			})
			var failure *provider.Failure
			if !errors.As(err, &failure) || failure.UnknownUsage != test.unknown ||
				out.Usage.InputTokens != test.input {
				t.Fatalf("wrong compact failure accounting: %+v %v", out, err)
			}
		})
	}
}

func TestLostCompactResponseKeepsUncertainReservation(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	client, err := provider.NewResponses(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = client.Compact(
		ctx,
		provider.Request{Model: "test", Input: []provider.Item{provider.Message("user", "hello")}},
	)
	close(release)
	var failure *provider.Failure
	if !errors.As(err, &failure) || !failure.UnknownUsage {
		t.Fatalf("lost response must keep reservation: %v", err)
	}
}

func TestCompactRetainsReasoningAndToolHistoryWithoutDispatch(t *testing.T) {
	t.Parallel()
	const window = `[{"type":"compaction","id":"cmp","encrypted_content":"opaque"},{"type":"reasoning","id":"r","summary":[]},{"type":"function_call","call_id":"c","name":"script","arguments":"{}"},{"type":"function_call_output","call_id":"c","output":"done"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"retained"}]}]`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(
			[]byte(
				`{"id":"compact","object":"response.compaction","output":` + window + `,"usage":{"input_tokens":10,"output_tokens":2}}`,
			),
		)
	}))
	defer server.Close()
	client, err := provider.NewResponses(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	out, err := client.Compact(
		context.Background(),
		provider.Request{Model: "test", Input: []provider.Item{provider.Message("user", "hello")}},
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(out.Output)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != window || len(out.Calls) != 0 || out.Text != "" {
		t.Fatalf("canonical history changed or redispatched: %+v", out)
	}
}
