// Package provider translates model calls without exposing credentials to scripts.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	codeInvalidRequest  = "invalid_request"
	codeInvalidResponse = "invalid_response"
	typeRefusal         = "refusal"
	fieldType           = "type"
	codeOutputLimit     = "output_limit"
	codeCodexSetup      = "codex_setup"
)

type Item = json.RawMessage
type Request struct {
	Provider        string `json:"Provider"`
	Model           string `json:"Model"`
	Effort          string `json:"Effort"`
	Instructions    string `json:"Instructions"`
	ClientRequestID string `json:"ClientRequestID"`
	Input           []Item `json:"Input"`
	Tools           []Tool `json:"Tools"`
	MaxOutputTokens int    `json:"MaxOutputTokens"`
}
type Tool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict"`
}
type Call struct {
	ID        string          `json:"ID"`
	Name      string          `json:"Name"`
	Arguments json.RawMessage `json:"Arguments"`
}
type Usage struct {
	InputTokens       int64 `json:"InputTokens"`
	OutputTokens      int64 `json:"OutputTokens"`
	CachedInputTokens int64 `json:"CachedInputTokens"`
	CacheWriteTokens  int64 `json:"CacheWriteTokens"`
}
type Response struct {
	Refused   bool   `json:"Refused"`
	ErrorCode string `json:"ErrorCode"`
	ID        string `json:"ID"`
	Output    []Item `json:"Output"`
	Text      string `json:"Text"`
	Calls     []Call `json:"Calls"`
	Usage     Usage  `json:"Usage"`
}
type Client interface {
	Respond(context.Context, Request) (Response, error)
}

// FailureError reports dispatch failure. UnknownUsage keeps a possibly billed reservation.
type FailureError struct {
	Code          string
	Status        int
	RetryAfter    time.Duration
	RetryAttempts int
	UnknownUsage  bool
}

func (e *FailureError) Error() string { return "provider: " + e.Code }

// Failure preserves the existing provider error API.
type Failure = FailureError //nolint:errname // Preserve the callback and persisted execution API.

type Responses struct {
	endpoint, apiKey string
	client           *http.Client
}

func NewResponses(endpoint, apiKey string, client *http.Client) (*Responses, error) {
	if endpoint == "" {
		endpoint = "https://api.openai.com/v1"
	}
	if !strings.HasPrefix(endpoint, "https://") && !strings.HasPrefix(endpoint, "http://") {
		return nil, errors.New("provider endpoint must be HTTP(S)")
	}
	u, parseErr := url.Parse(endpoint)
	if parseErr != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid provider endpoint")
	}
	if apiKey == "" {
		return nil, errors.New("provider API key is required")
	}
	if client == nil {
		client = &http.Client{}
	}
	c := *client
	c.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &Responses{strings.TrimRight(endpoint, "/") + "/responses", apiKey, &c}, nil
}
func (p *Responses) Respond(ctx context.Context, in Request) (Response, error) {
	if in.Model == "" || in.MaxOutputTokens <= 0 || len(in.Input) == 0 {
		return Response{}, &Failure{Code: codeInvalidRequest}
	}
	body := map[string]any{
		"model":               in.Model,
		"input":               in.Input,
		"store":               false,
		"max_output_tokens":   in.MaxOutputTokens,
		"parallel_tool_calls": true,
		"include":             []string{"reasoning.encrypted_content"},
	}
	if in.Provider == "openai" {
		body["service_tier"] = "default"
	}
	if in.Instructions != "" {
		body["instructions"] = in.Instructions
	}
	if in.Effort != "" {
		body["reasoning"] = map[string]string{"effort": in.Effort}
	}
	if len(in.Tools) > 0 {
		body["tools"] = in.Tools
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return Response{}, fmt.Errorf("encode provider request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(encoded))
	if err != nil {
		return Response{}, &Failure{Code: "invalid_endpoint"}
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", "application/json")
	if in.ClientRequestID != "" {
		req.Header.Set("X-Client-Request-Id", in.ClientRequestID)
	}
	res, err := p.client.Do(req)
	if err != nil {
		return Response{}, &Failure{Code: "transport_failure", UnknownUsage: true}
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return Response{}, httpFailure(res)
	}
	out, status, decodeErr := decodeResponse(res.Body)
	if decodeErr != nil {
		return out, decodeErr
	}

	if outputErr := decodeOutputs(&out); outputErr != nil {
		return out, outputErr
	}
	if status == "incomplete" {
		return out, &Failure{Code: codeOutputLimit}
	}
	return out, nil
}

func decodeResponse(body io.Reader) (Response, string, error) {
	var wire struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Output []Item `json:"output"`
		Usage  *struct {
			Input   *int64 `json:"input_tokens"`
			Output  *int64 `json:"output_tokens"`
			Details struct {
				Cached  int64 `json:"cached_tokens"`
				Written int64 `json:"cache_write_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
	}
	data, err := io.ReadAll(io.LimitReader(body, developmentLimit+1))
	if err != nil || len(data) > developmentLimit {
		return Response{}, "", &Failure{Code: "response_unreadable", UnknownUsage: true}
	}
	if json.Unmarshal(data, &wire) != nil || wire.ID == "" || wire.Usage == nil || wire.Usage.Input == nil ||
		wire.Usage.Output == nil ||
		(wire.Status != "completed" && wire.Status != "incomplete") {
		return Response{}, "", &Failure{Code: codeInvalidResponse, UnknownUsage: true}
	}
	if *wire.Usage.Input < 0 || *wire.Usage.Output < 0 || wire.Usage.Details.Cached < 0 ||
		wire.Usage.Details.Cached > *wire.Usage.Input ||
		wire.Usage.Details.Written < 0 ||
		wire.Usage.Details.Written > *wire.Usage.Input-wire.Usage.Details.Cached {
		return Response{}, "", &Failure{Code: "invalid_usage", UnknownUsage: true}
	}
	out := Response{
		ID:     wire.ID,
		Output: wire.Output,
		Usage: Usage{
			InputTokens:       *wire.Usage.Input,
			OutputTokens:      *wire.Usage.Output,
			CachedInputTokens: wire.Usage.Details.Cached,
			CacheWriteTokens:  wire.Usage.Details.Written,
		},
	}
	return out, wire.Status, nil
}

func Message(role, text string) Item {
	raw, _ := json.Marshal(map[string]any{"role": role, "content": text})
	return raw
}
func CallOutput(id string, out json.RawMessage) Item {
	raw, _ := json.Marshal(map[string]any{fieldType: "function_call_output", "call_id": id, "output": string(out)})
	return raw
}

func decodeOutputs(out *Response) error {
	for _, raw := range out.Output {
		var item struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Content   []struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Refusal string `json:"refusal"`
			} `json:"content"`
		}
		if json.Unmarshal(raw, &item) != nil {
			return &Failure{Code: "invalid_output", UnknownUsage: false}
		}
		switch item.Type {
		case "function_call":
			if item.CallID == "" || item.Name == "" || !json.Valid([]byte(item.Arguments)) {
				return &Failure{Code: "invalid_tool_call", UnknownUsage: false}
			}
			out.Calls = append(out.Calls, Call{item.CallID, item.Name, json.RawMessage(item.Arguments)})
		case "message":
			for _, c := range item.Content {
				switch c.Type {
				case "output_text":
					out.Text += c.Text
				case typeRefusal:
					out.Text += c.Refusal
					out.Refused = true
				}
			}
		}
	}
	return nil
}
