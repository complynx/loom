package provider_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/complynx/loom/internal/provider"
)

func TestInputCountingIncludesVisionAndTools(t *testing.T) {
	t.Parallel()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses/input_tokens" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("wrong count endpoint")
		}
		var input map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			t.Error("invalid count input")
		}
		if len(input["tools"]) == 0 || len(input["input"]) == 0 {
			t.Error("dropped count context")
		}
		_, _ = w.Write([]byte(`{"object":"response.input_tokens","input_tokens":4096}`))
	}))
	defer s.Close()
	p, _ := provider.NewResponses(s.URL, "secret", s.Client())
	count, err := p.CountInputTokens(
		context.Background(),
		provider.Request{
			Model: "test",
			Input: []provider.Item{
				json.RawMessage(
					`{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,fixture"}]}`,
				),
			},
			Tools: []provider.Tool{{Type: "function", Name: "f", Parameters: json.RawMessage(`{}`)}},
		},
	)
	if err != nil || count != 4096 {
		t.Fatalf("wrong count %d %v", count, err)
	}
}
