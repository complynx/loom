package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// TokenCounter obtains provider-accurate input counts, including vision and tool formatting.
type TokenCounter interface {
	CountInputTokens(context.Context, Request) (int64, error)
}

func (p *Responses) CountInputTokens(ctx context.Context, in Request) (int64, error) {
	body := map[string]any{"model": in.Model, "input": in.Input, "instructions": in.Instructions}
	if len(in.Tools) > 0 {
		body["tools"] = in.Tools
	}
	if in.Effort != "" {
		body["reasoning"] = map[string]string{"effort": in.Effort}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, errors.New("invalid token count input")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint+"/input_tokens", bytes.NewReader(raw))
	if err != nil {
		return 0, errors.New("invalid token count endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := p.client.Do(req)
	if err != nil {
		return 0, errors.New("token count unavailable")
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return 0, errors.New("token count unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 4097))
	if err != nil || len(data) > 4096 {
		return 0, errors.New("invalid token count response")
	}
	var out struct {
		Object string `json:"object"`
		Count  *int64 `json:"input_tokens"`
	}
	if json.Unmarshal(data, &out) != nil || out.Object != "response.input_tokens" || out.Count == nil || *out.Count < 0 {
		return 0, errors.New("invalid token count response")
	}
	return *out.Count, nil
}
