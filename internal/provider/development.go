package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"
)

const (
	developmentLimit = 8 << 20
	mockPollInterval = 50 * time.Millisecond
)

type mock struct{ dir string }

// NewMock uses an operator-provisioned directory for manual QA request/response files.
func NewMock(dir string) (Client, error) {
	if err := developmentDirectory(dir); err != nil {
		return nil, err
	}
	return &mock{dir: dir}, nil
}

func developmentDirectory(dir string) error {
	if !filepath.IsAbs(dir) {
		return errors.New("development provider directory must be absolute")
	}
	s, err := os.Stat(dir)
	if err != nil || !s.IsDir() {
		return errors.New("development provider directory must exist")
	}
	return nil
}

func developmentRequest(in Request) ([]byte, error) {
	if in.ClientRequestID == "" || in.Model == "" || in.MaxOutputTokens <= 0 || len(in.Input) == 0 {
		return nil, &Failure{Code: codeInvalidRequest}
	}
	b, err := json.Marshal(in)
	if err != nil || len(b) > developmentLimit {
		return nil, &Failure{Code: codeInvalidRequest}
	}
	return b, nil
}

func (p *mock) Respond(ctx context.Context, in Request) (Response, error) {
	b, err := developmentRequest(in)
	if err != nil {
		return Response{}, err
	}
	if ctx.Err() != nil {
		return Response{}, ctx.Err()
	}
	hash := sha256.Sum256([]byte(in.ClientRequestID))
	base := filepath.Join(p.dir, hex.EncodeToString(hash[:]))
	f, err := os.CreateTemp(p.dir, ".request-*")
	if err != nil {
		return Response{}, &Failure{Code: "mock_request_write"}
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	_, err = f.Write(b)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return Response{}, &Failure{Code: "mock_request_write"}
	}
	// A hard link publishes a complete file without replacing an existing request.
	if err = os.Link(name, base+".request.json"); err != nil {
		old, readErr := developmentRead(base + ".request.json")
		if readErr != nil || !bytes.Equal(old, b) {
			return Response{}, &Failure{Code: "mock_request_conflict"}
		}
	}
	ticker := time.NewTicker(mockPollInterval)
	defer ticker.Stop()
	for {
		data, readErr := developmentRead(base + ".response.json")
		if readErr == nil {
			var out Response
			var wire struct {
				Usage *struct {
					InputTokens       *int64 `json:"InputTokens"`
					OutputTokens      *int64 `json:"OutputTokens"`
					CachedInputTokens *int64 `json:"CachedInputTokens"`
					CacheWriteTokens  *int64 `json:"CacheWriteTokens"`
				} `json:"Usage"`
			}
			if json.Unmarshal(data, &out) != nil || json.Unmarshal(data, &wire) != nil || wire.Usage == nil ||
				wire.Usage.InputTokens == nil ||
				wire.Usage.OutputTokens == nil ||
				wire.Usage.CachedInputTokens == nil ||
				wire.Usage.CacheWriteTokens == nil {
				return Response{}, &Failure{Code: codeInvalidResponse, UnknownUsage: true}
			}
			return developmentResponse(in, out)
		}
		if !os.IsNotExist(readErr) {
			return Response{}, &Failure{Code: "response_unreadable", UnknownUsage: true}
		}
		select {
		case <-ctx.Done():
			return Response{}, &Failure{Code: "mock_cancelled", UnknownUsage: true}
		case <-ticker.C:
		}
	}
}

func developmentRead(path string) ([]byte, error) {
	f, err := os.Open(path) // #nosec G304 -- Operator directory plus hashed request ID; no user path components.
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, developmentLimit+1))
	if err == nil && len(b) > developmentLimit {
		err = errors.New("development provider file too large")
	}
	return b, err
}

func developmentResponse(in Request, out Response) (Response, error) {
	u := out.Usage
	if u.InputTokens < 0 || u.OutputTokens < 0 || u.CachedInputTokens < 0 || u.CacheWriteTokens < 0 ||
		u.CachedInputTokens > u.InputTokens ||
		u.CacheWriteTokens > u.InputTokens-u.CachedInputTokens {
		return Response{}, &Failure{Code: "invalid_usage", UnknownUsage: true}
	}
	if out.ID == "" {
		return Response{}, &Failure{Code: codeInvalidResponse, UnknownUsage: true}
	}
	var output []Item
	if out.Text != "" {
		kind := "output_text"
		field := "text"
		if out.Refused {
			kind, field = typeRefusal, typeRefusal
		}
		b, _ := json.Marshal(
			map[string]any{
				fieldType: "message",
				"role":    "assistant",
				"content": []any{map[string]string{fieldType: kind, field: out.Text}},
			},
		)
		output = append(output, b)
	}
	names := map[string]bool{}
	for _, t := range in.Tools {
		if t.Type == "function" {
			names[t.Name] = true
		}
	}
	ids := map[string]bool{}
	for _, c := range out.Calls {
		var arguments map[string]json.RawMessage
		if c.ID == "" || ids[c.ID] || !names[c.Name] || json.Unmarshal(c.Arguments, &arguments) != nil ||
			arguments == nil ||
			out.Refused ||
			out.ErrorCode != "" {
			return Response{ID: out.ID, Usage: u}, &Failure{Code: "invalid_tool_call"}
		}
		ids[c.ID] = true
		b, _ := json.Marshal(
			map[string]any{
				fieldType:   "function_call",
				"call_id":   c.ID,
				"name":      c.Name,
				"arguments": string(c.Arguments),
			},
		)
		output = append(output, b)
	}
	if len(out.Output) > 0 {
		a, _ := json.Marshal(out.Output)
		b, _ := json.Marshal(output)
		var supplied, canonical any
		if json.Unmarshal(a, &supplied) != nil || json.Unmarshal(b, &canonical) != nil ||
			!reflect.DeepEqual(supplied, canonical) {
			return Response{ID: out.ID, Usage: u}, &Failure{Code: "invalid_output"}
		}
	}
	out.Output = output
	if out.Usage.OutputTokens > int64(in.MaxOutputTokens) || out.ErrorCode == codeOutputLimit {
		return out, &Failure{Code: codeOutputLimit}
	}
	if out.ErrorCode != "" {
		return out, &Failure{Code: out.ErrorCode}
	}
	return out, nil
}
