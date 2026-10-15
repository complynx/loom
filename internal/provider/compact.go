package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const (
	fieldModel             = "model"
	fieldInput             = "input"
	codeResponseUnreadable = "response_unreadable"
	typeMessage            = "message"
)

// CompactClient returns a replacement input window, including opaque compaction items.
// Output must be saved and reused as context. It is not a new model turn.
type CompactClient interface {
	Compact(context.Context, Request) (Response, error)
}

// Compact calls the stateless compact endpoint. Reasoning, tools, output bounds,
// and store are not compact endpoint parameters.
func (p *Responses) Compact(ctx context.Context, in Request) (Response, error) {
	if in.Model == "" || len(in.Input) == 0 {
		return Response{}, &Failure{Code: codeInvalidRequest}
	}
	body := map[string]any{fieldModel: in.Model, fieldInput: in.Input}
	if in.Instructions != "" {
		body["instructions"] = in.Instructions
	}
	if in.Provider == "openai" {
		body["service_tier"] = "default"
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return Response{}, fmt.Errorf("encode compact request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint+"/compact", bytes.NewReader(encoded))
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
	return decodeCompactResponse(res.Body)
}

func compactHTTPFailure(status int) *Failure {
	known := false
	switch status {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusPaymentRequired,
		http.StatusForbidden, http.StatusNotFound, http.StatusMethodNotAllowed,
		http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType,
		http.StatusUnprocessableEntity, http.StatusTooManyRequests:
		known = true
	}
	return &Failure{Code: "http_error", Status: status, UnknownUsage: !known}
}

func decodeCompactResponse(body io.Reader) (Response, error) {
	data, err := io.ReadAll(io.LimitReader(body, developmentLimit+1))
	if err != nil || len(data) > developmentLimit {
		return Response{}, &Failure{Code: codeResponseUnreadable, UnknownUsage: true}
	}
	var wire map[string]json.RawMessage
	if json.Unmarshal(data, &wire) != nil || wire == nil {
		return Response{}, &Failure{Code: codeInvalidResponse, UnknownUsage: true}
	}
	// Compact has no status field. Reuse the response usage decoder without
	// decoding outputs as tool calls or assistant messages.
	wire["status"] = json.RawMessage(`"completed"`)
	normalized, err := json.Marshal(wire)
	if err != nil {
		return Response{}, fmt.Errorf("normalize compact response: %w", err)
	}
	out, _, err := decodeResponse(bytes.NewReader(normalized))
	if err != nil {
		return out, err
	}
	var object string
	if json.Unmarshal(wire["object"], &object) != nil || object != "response.compaction" ||
		!validCompactWindow(out.Output) {
		return out, &Failure{Code: codeInvalidResponse}
	}
	return out, nil
}

func validCompactWindow(items []Item) bool {
	compactions := 0
	for _, raw := range items {
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil || object == nil {
			return false
		}
		var kind string
		if json.Unmarshal(object["type"], &kind) != nil || kind == "" {
			return false
		}
		if kind != "compaction" {
			continue
		}
		var id, encrypted string
		if json.Unmarshal(object["id"], &id) != nil || id == "" ||
			json.Unmarshal(object["encrypted_content"], &encrypted) != nil || encrypted == "" {
			return false
		}
		compactions++
	}
	return compactions > 0
}
