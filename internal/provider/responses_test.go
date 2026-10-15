package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/complynx/loom/internal/provider"
)

func TestResponsesPreservesToolAndReasoningContext(t *testing.T) {
	t.Parallel()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("wrong authenticated endpoint")
		}
		var in map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Fatal(err)
		}
		if _, leaked := in["Provider"]; leaked {
			t.Error("routing metadata leaked to provider")
		}
		if _, leaked := in["provider"]; leaked {
			t.Error("routing metadata leaked to provider")
		}
		if string(in["store"]) != "false" || string(in["max_output_tokens"]) != "100" {
			t.Error("missing bounds")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(
			[]byte(
				`{"id":"r1","status":"completed","output":[{"type":"reasoning","encrypted_content":"opaque"},{"type":"function_call","call_id":"c1","name":"script","arguments":"{\"code\":\"return 1\"}"}],"usage":{"input_tokens":20,"output_tokens":10,"input_tokens_details":{"cached_tokens":5}}}`,
			),
		)
	}))
	defer s.Close()
	p, err := provider.NewResponses(s.URL, "secret", s.Client())
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.Respond(
		context.Background(),
		provider.Request{
			Provider:        "private-routing-alias",
			Model:           "test",
			Input:           []provider.Item{provider.Message("user", "hello")},
			MaxOutputTokens: 100,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Output) != 2 || len(out.Calls) != 1 || out.Calls[0].ID != "c1" || out.Usage.CachedInputTokens != 5 {
		t.Fatalf("wrong result: %+v", out)
	}
}
func TestLostResponseRetainsUncertainUsage(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer s.Close()
	p, _ := provider.NewResponses(s.URL, "secret", s.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := p.Respond(
		ctx,
		provider.Request{
			Provider:        "private-routing-alias",
			Model:           "test",
			Input:           []provider.Item{provider.Message("user", "hello")},
			MaxOutputTokens: 100,
		},
	)
	close(release)
	var f *provider.Failure
	if !errors.As(err, &f) || !f.UnknownUsage {
		t.Fatalf("must preserve reserve: %v", err)
	}
}
func TestIncompleteResponseStillAccountsUsage(t *testing.T) {
	t.Parallel()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(
			[]byte(`{"id":"r1","status":"incomplete","output":[],"usage":{"input_tokens":20,"output_tokens":100}}`),
		)
	}))
	defer s.Close()
	p, _ := provider.NewResponses(s.URL, "secret", s.Client())
	out, err := p.Respond(
		context.Background(),
		provider.Request{
			Provider:        "private-routing-alias",
			Model:           "test",
			Input:           []provider.Item{provider.Message("user", "hello")},
			MaxOutputTokens: 100,
		},
	)
	if err == nil || out.Usage.OutputTokens != 100 || out.ID != "r1" {
		t.Fatalf("lost billable usage: %+v %v", out, err)
	}
}
func TestRedirectDoesNotDiscloseCredentials(t *testing.T) {
	t.Parallel()
	called := false
	dest := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true }))
	defer dest.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dest.URL, http.StatusTemporaryRedirect)
	}))
	defer s.Close()
	p, _ := provider.NewResponses(s.URL, "secret", s.Client())
	_, err := p.Respond(
		context.Background(),
		provider.Request{
			Provider:        "private-routing-alias",
			Model:           "test",
			Input:           []provider.Item{provider.Message("user", "hello")},
			MaxOutputTokens: 100,
		},
	)
	if err == nil || called {
		t.Fatalf("followed unsafe redirect: %v", err)
	}
}

func TestHTTPFailureDistinguishesRejectedFromUncertainDispatch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status  int
		unknown bool
	}{{http.StatusUnauthorized, false}, {http.StatusTooManyRequests, false}, {http.StatusRequestTimeout, true}, {http.StatusInternalServerError, true}} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			t.Parallel()
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte("never expose upstream sensitive body"))
			}))
			defer s.Close()
			p, _ := provider.NewResponses(s.URL, "secret", s.Client())
			_, err := p.Respond(
				context.Background(),
				provider.Request{
					Provider:        "private-routing-alias",
					Model:           "test",
					Input:           []provider.Item{provider.Message("user", "hello")},
					MaxOutputTokens: 100,
				},
			)
			var f *provider.Failure
			if !errors.As(err, &f) || f.UnknownUsage != tc.unknown {
				t.Fatalf("wrong billing certainty: %v", err)
			}
			if f.Error() != "provider: http_error" {
				t.Fatal("upstream body leaked")
			}
		})
	}
}

func TestOpenAIStandardTierAndCacheWriteUsage(t *testing.T) {
	t.Parallel()
	for _, written := range []int64{10, 16} {
		t.Run(strconv.FormatInt(written, 10), func(t *testing.T) { t.Parallel(); checkCacheWriteUsage(t, written) })
	}
}
func checkCacheWriteUsage(t *testing.T, written int64) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["service_tier"] != "default" {
			t.Error("OpenAI must use configured Standard tariff")
		}
		_ = json.NewEncoder(w).
			Encode(map[string]any{"id": "response", "status": "completed", "output": []any{}, "usage": map[string]any{"input_tokens": 20, "output_tokens": 5, "input_tokens_details": map[string]int64{"cached_tokens": 5, "cache_write_tokens": written}}})
	}))
	defer server.Close()
	client, err := provider.NewResponses(server.URL, "test-only", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Respond(
		context.Background(),
		provider.Request{
			Provider:        "openai",
			Model:           "gpt-6-luna",
			Input:           []provider.Item{provider.Message("user", "test")},
			MaxOutputTokens: 100,
		},
	)
	if written != 10 {
		var failure *provider.Failure
		if !errors.As(err, &failure) || !failure.UnknownUsage {
			t.Fatalf("invalid usage must retain reserve: %v", err)
		}
		return
	}
	if err != nil || result.Usage.CacheWriteTokens != 10 {
		t.Fatalf("lost write usage: %+v %v", result, err)
	}
}

func TestResponsesRejectsMissingRequiredUsage(t *testing.T) {
	t.Parallel()
	for _, usage := range []string{`{}`, `{"input_tokens":1}`, `{"output_tokens":1}`, `{"input_tokens":null,"output_tokens":1}`} {
		t.Run(usage, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"id":"response","status":"completed","output":[],"usage":` + usage + `}`))
			}))
			defer server.Close()
			client, err := provider.NewResponses(server.URL, "fixture-key", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Respond(
				context.Background(),
				provider.Request{
					Model:           "fixture",
					Input:           []provider.Item{provider.Message("user", "fixture")},
					MaxOutputTokens: 10,
				},
			)
			var failure *provider.Failure
			if !errors.As(err, &failure) || !failure.UnknownUsage {
				t.Fatalf("missing usage must keep billing uncertain: %v", err)
			}
			_, err = client.Compact(
				context.Background(),
				provider.Request{
					Model:           "fixture",
					Input:           []provider.Item{provider.Message("user", "fixture")},
					MaxOutputTokens: 10,
				},
			)
			if !errors.As(err, &failure) || !failure.UnknownUsage {
				t.Fatalf("missing compaction usage must keep billing uncertain: %v", err)
			}
		})
	}
}
