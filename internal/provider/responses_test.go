package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestResponsesPreservesToolAndReasoningContext(t *testing.T) {
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
		_, _ = w.Write([]byte(`{"id":"r1","status":"completed","output":[{"type":"reasoning","encrypted_content":"opaque"},{"type":"function_call","call_id":"c1","name":"script","arguments":"{\"code\":\"return 1\"}"}],"usage":{"input_tokens":20,"output_tokens":10,"input_tokens_details":{"cached_tokens":5}}}`))
	}))
	defer s.Close()
	p, err := NewResponses(s.URL, "secret", s.Client())
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.Respond(context.Background(), Request{Provider: "private-routing-alias", Model: "test", Input: []Item{Message("user", "hello")}, MaxOutputTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Output) != 2 || len(out.Calls) != 1 || out.Calls[0].ID != "c1" || out.Usage.CachedInputTokens != 5 {
		t.Fatalf("wrong result: %+v", out)
	}
}
func TestLostResponseRetainsUncertainUsage(t *testing.T) {
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer s.Close()
	p, _ := NewResponses(s.URL, "secret", s.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := p.Respond(ctx, Request{Provider: "private-routing-alias", Model: "test", Input: []Item{Message("user", "hello")}, MaxOutputTokens: 100})
	close(release)
	var f *Failure
	if !errors.As(err, &f) || !f.UnknownUsage {
		t.Fatalf("must preserve reserve: %v", err)
	}
}
func TestIncompleteResponseStillAccountsUsage(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"r1","status":"incomplete","output":[],"usage":{"input_tokens":20,"output_tokens":100}}`))
	}))
	defer s.Close()
	p, _ := NewResponses(s.URL, "secret", s.Client())
	out, err := p.Respond(context.Background(), Request{Provider: "private-routing-alias", Model: "test", Input: []Item{Message("user", "hello")}, MaxOutputTokens: 100})
	if err == nil || out.Usage.OutputTokens != 100 || out.ID != "r1" {
		t.Fatalf("lost billable usage: %+v %v", out, err)
	}
}
func TestRedirectDoesNotDiscloseCredentials(t *testing.T) {
	called := false
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer dest.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dest.URL, http.StatusTemporaryRedirect)
	}))
	defer s.Close()
	p, _ := NewResponses(s.URL, "secret", s.Client())
	_, err := p.Respond(context.Background(), Request{Provider: "private-routing-alias", Model: "test", Input: []Item{Message("user", "hello")}, MaxOutputTokens: 100})
	if err == nil || called {
		t.Fatalf("followed unsafe redirect: %v", err)
	}
}

func TestHTTPFailureDistinguishesRejectedFromUncertainDispatch(t *testing.T) {
	for _, tc := range []struct {
		status  int
		unknown bool
	}{{http.StatusUnauthorized, false}, {http.StatusTooManyRequests, false}, {http.StatusRequestTimeout, true}, {http.StatusInternalServerError, true}} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte("never expose upstream sensitive body"))
			}))
			defer s.Close()
			p, _ := NewResponses(s.URL, "secret", s.Client())
			_, err := p.Respond(context.Background(), Request{Provider: "private-routing-alias", Model: "test", Input: []Item{Message("user", "hello")}, MaxOutputTokens: 100})
			var f *Failure
			if !errors.As(err, &f) || f.UnknownUsage != tc.unknown {
				t.Fatalf("wrong billing certainty: %v", err)
			}
			if f.Error() != "provider: http_error" {
				t.Fatal("upstream body leaked")
			}
		})
	}
}
