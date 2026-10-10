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
)

type Item = json.RawMessage
type Request struct {
	Provider                    string
	Model, Effort, Instructions string
	ClientRequestID             string
	Input                       []Item
	Tools                       []Tool
	MaxOutputTokens             int
}
type Tool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict"`
}
type Call struct {
	ID, Name  string
	Arguments json.RawMessage
}
type Usage struct{ InputTokens, OutputTokens, CachedInputTokens int64 }
type Response struct {
	Refused   bool
	ErrorCode string
	ID        string
	Output    []Item
	Text      string
	Calls     []Call
	Usage     Usage
}
type Client interface {
	Respond(context.Context, Request) (Response, error)
}

// UnknownUsage keeps the reservation when dispatch may have been billed.
type Failure struct {
	Code         string
	Status       int
	UnknownUsage bool
}

func (e *Failure) Error() string { return "provider: " + e.Code }

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
		return Response{}, &Failure{Code: "invalid_request"}
	}
	body := map[string]any{"model": in.Model, "input": in.Input, "store": false, "max_output_tokens": in.MaxOutputTokens, "parallel_tool_calls": true, "include": []string{"reasoning.encrypted_content"}}
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
		req.Header.Set("X-Client-Request-ID", in.ClientRequestID)
	}
	res, err := p.client.Do(req)
	if err != nil {
		return Response{}, &Failure{Code: "transport_failure", UnknownUsage: true}
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		knownRejection := false
		switch res.StatusCode {
		case 400, 401, 402, 403, 404, 405, 413, 415, 422, 429:
			knownRejection = true
		}
		return Response{}, &Failure{Code: "http_error", Status: res.StatusCode, UnknownUsage: !knownRejection}
	}
	var wire struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Output []Item `json:"output"`
		Usage  *struct {
			Input   int64 `json:"input_tokens"`
			Output  int64 `json:"output_tokens"`
			Details struct {
				Cached int64 `json:"cached_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
	if err != nil || len(data) > 8<<20 {
		return Response{}, &Failure{Code: "response_unreadable", UnknownUsage: true}
	}
	if json.Unmarshal(data, &wire) != nil || wire.ID == "" || wire.Usage == nil || (wire.Status != "completed" && wire.Status != "incomplete") {
		return Response{}, &Failure{Code: "invalid_response", UnknownUsage: true}
	}
	if wire.Usage.Input < 0 || wire.Usage.Output < 0 || wire.Usage.Details.Cached < 0 || wire.Usage.Details.Cached > wire.Usage.Input {
		return Response{}, &Failure{Code: "invalid_usage", UnknownUsage: true}
	}
	out := Response{ID: wire.ID, Output: wire.Output, Usage: Usage{wire.Usage.Input, wire.Usage.Output, wire.Usage.Details.Cached}}
	for _, raw := range wire.Output {
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
			return out, &Failure{Code: "invalid_output", UnknownUsage: false}
		}
		switch item.Type {
		case "function_call":
			if item.CallID == "" || item.Name == "" || !json.Valid([]byte(item.Arguments)) {
				return out, &Failure{Code: "invalid_tool_call", UnknownUsage: false}
			}
			out.Calls = append(out.Calls, Call{item.CallID, item.Name, json.RawMessage(item.Arguments)})
		case "message":
			for _, c := range item.Content {
				switch c.Type {
				case "output_text":
					out.Text += c.Text
				case "refusal":
					out.Text += c.Refusal
					out.Refused = true
				}
			}
		}
	}
	if wire.Status == "incomplete" {
		return out, &Failure{Code: "output_limit"}
	}
	return out, nil
}
func Message(role, text string) Item {
	raw, _ := json.Marshal(map[string]any{"role": role, "content": text})
	return raw
}
func CallOutput(id string, out json.RawMessage) Item {
	raw, _ := json.Marshal(map[string]any{"type": "function_call_output", "call_id": id, "output": string(out)})
	return raw
}
