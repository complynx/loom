package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/provider"
	"github.com/complynx/loom/internal/sandbox"
	"github.com/complynx/loom/internal/telemetry"
)

type actionLogProvider struct {
	request  provider.Request
	response provider.Response
}

func (p *actionLogProvider) Respond(_ context.Context, request provider.Request) (provider.Response, error) {
	p.request = request
	return p.response, nil
}
func (p *actionLogProvider) Compact(ctx context.Context, request provider.Request) (provider.Response, error) {
	return p.Respond(ctx, request)
}
func (p *actionLogProvider) CountInputTokens(_ context.Context, _ provider.Request) (int64, error) {
	return 99, nil
}

func TestModelActionLogKeepsUsefulPayloadsWithoutHistory(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "actions.jsonl")
	observer := telemetry.New(slog.New(slog.DiscardHandler))
	closeLog, err := observer.EnableActionLog(config.ActionLog{Enabled: true, Path: path, MaxSizeMB: 1, MaxBackups: 2})
	require.NoError(t, err)
	t.Cleanup(func() { _ = closeLog() })
	client := &actionLogProvider{
		response: provider.Response{
			ID:    "response-id",
			Text:  strings.Repeat("large output ", 1000),
			Usage: provider.Usage{InputTokens: 20},
		},
	}
	p := measuredProvider{client: client, observer: observer}
	request := provider.Request{
		Model:           "model",
		ClientRequestID: "step-id",
		Instructions:    "effective instructions",
		MaxOutputTokens: 4096,
		Input:           []provider.Item{provider.Message("user", strings.Repeat("history-only-text", 10000))},
		Tools: []provider.Tool{
			{Name: "script", Description: "script contract", Parameters: json.RawMessage(`{"type":"object"}`)},
		},
	}
	ctx := telemetry.WithRun(t.Context(), "run-id")
	response, err := p.Respond(ctx, request)
	require.NoError(t, err)
	require.Equal(t, client.response, response)
	require.Equal(t, request, client.request)
	_, err = p.Compact(ctx, request)
	require.NoError(t, err)
	count, err := p.CountInputTokens(ctx, request)
	require.NoError(t, err)
	require.EqualValues(t, 99, count)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "history-only-text")
	for _, wanted := range []string{"effective instructions", "script contract", "step-id", "run-id", "model.respond", "model.compact", "model.count_tokens"} {
		require.Contains(t, string(raw), wanted)
	}
	hash := sha256.Sum256(request.Input[0])
	require.Contains(t, string(raw), hex.EncodeToString(hash[:]))
	lines := bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})
	require.Len(t, lines, 6)
	var completed struct {
		Data struct {
			Output provider.Response `json:"output"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(lines[1], &completed))
	require.Equal(t, client.response, completed.Data.Output)
}

func TestScriptActionLogKeepsInnerRPCAndErrors(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "actions.jsonl")
	observer := telemetry.New(slog.New(slog.DiscardHandler))
	closeLog, err := observer.EnableActionLog(config.ActionLog{Enabled: true, Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = closeLog() })
	wantedErr := errors.New("private backend error")
	broker := loggedScriptBroker(
		observer,
		"run-id",
		sandbox.BrokerFunc(
			func(_ context.Context, module, method string, args json.RawMessage) (json.RawMessage, error) {
				require.Equal(t, "files", module)
				require.JSONEq(t, `{"handle":"file-1"}`, string(args))
				if method == "fail" {
					return nil, wantedErr
				}
				return json.RawMessage(`{"text":"tool result"}`), nil
			},
		),
	)
	ctx, done := observer.BeginAction(telemetry.WithRun(t.Context(), "run-id"), "tool", nil)
	result, err := broker.Call(ctx, "files", "read", json.RawMessage(`{"handle":"file-1"}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"text":"tool result"}`, string(result))
	_, err = broker.Call(ctx, "files", "fail", json.RawMessage(`{"handle":"file-1"}`))
	require.ErrorIs(t, err, wantedErr)
	done(result, err)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(raw), "script_rpc")
	require.Contains(t, string(raw), "tool result")
	require.Contains(t, string(raw), `"outcome":"error"`)
	require.NotContains(t, string(raw), wantedErr.Error())
}
