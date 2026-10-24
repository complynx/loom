package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
)

func TestActionLogLargeNestedPayloadsAndDisabled(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "actions.jsonl")
	observer := New(slog.New(slog.DiscardHandler))
	closeLog, err := observer.EnableActionLog(config.ActionLog{Path: path})
	require.NoError(t, err)
	ctx, done := observer.BeginAction(t.Context(), "disabled", "private")
	done(nil, nil)
	require.NoError(t, closeLog())
	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
	closeLog, err = observer.EnableActionLog(config.ActionLog{Enabled: true, Path: path, MaxSizeMB: 1, MaxBackups: 2})
	require.NoError(t, err)
	ctx, done = observer.BeginAction(WithRun(ctx, "run"), "tool", map[string]any{
		"text": strings.Repeat("данные", 4000), "arguments": `{"api_key":"secret-key","id":9007199254740993}`,
		"large_id": int64(9007199254740993),
	})
	_, nested := observer.BeginAction(ctx, "script_rpc", map[string]string{"password": "secret-password"})
	nested(map[string]any{"InputTokens": 123, "access_token": "secret-token"}, context.DeadlineExceeded)
	done(nil, errors.New("secret-error"))
	require.NoError(t, closeLog())
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, 2, strings.Count(string(raw), "9007199254740993"))
	for _, secret := range []string{"secret-key", "secret-password", "secret-token", "secret-error"} {
		require.NotContains(t, string(raw), secret)
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})
	require.Len(t, lines, 4)
	var records [4]struct {
		ActionID string `json:"action_id"`
		RunID    string `json:"run_id"`
		Data     struct {
			Parent  string `json:"parent_action_id"`
			Outcome string `json:"outcome"`
			Input   struct {
				Text string `json:"text"`
			} `json:"input"`
			Output struct {
				InputTokens int `json:"InputTokens"`
			} `json:"output"`
		} `json:"data"`
	}
	for i, line := range lines {
		require.NoError(t, json.Unmarshal(line, &records[i]))
		require.Equal(t, "run", records[i].RunID)
	}
	require.Equal(t, strings.Repeat("данные", 4000), records[0].Data.Input.Text)
	require.Equal(t, records[0].ActionID, records[1].Data.Parent)
	require.Equal(t, records[1].ActionID, records[2].ActionID)
	require.Equal(t, "timeout", records[2].Data.Outcome)
	require.Equal(t, 123, records[2].Data.Output.InputTokens)
	require.Equal(t, "error", records[3].Data.Outcome)
}

func TestActionLogRotationRestartAndConcurrentRecords(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "actions.jsonl")
	observer := New(slog.New(slog.DiscardHandler))
	closeLog, err := observer.EnableActionLog(config.ActionLog{Enabled: true, Path: path, MaxSizeMB: 1, MaxBackups: 2})
	require.NoError(t, err)
	// Force a boundary after every record, including oversized records.
	observer.actions.maxSize = 1
	for i := range 4 {
		observer.Action(t.Context(), "rotate", map[string]int{"index": i})
	}
	require.NoError(t, closeLog())
	files, err := filepath.Glob(path + "*")
	require.NoError(t, err)
	require.Len(t, files, 3)
	for i, name := range []string{path + ".2", path + ".1", path} {
		raw, readErr := os.ReadFile(name)
		require.NoError(t, readErr)
		var record struct {
			Data struct {
				Index int `json:"index"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(raw, &record))
		require.Equal(t, i+1, record.Data.Index)
	}
	closeLog, err = observer.EnableActionLog(config.ActionLog{Enabled: true, Path: path, MaxSizeMB: 1, MaxBackups: 2})
	require.NoError(t, err)
	var group sync.WaitGroup
	for range 30 {
		group.Go(func() { observer.Action(t.Context(), "parallel", map[string]string{"text": "result"}) })
	}
	group.Wait()
	require.NoError(t, closeLog())
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})
	require.Len(t, lines, 31)
	for _, line := range lines {
		require.True(t, json.Valid(line))
	}
}

func TestActionLogWriteFailureIsReportedOnce(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	observer := New(slog.New(slog.NewJSONHandler(&logs, nil)))
	path := filepath.Join(t.TempDir(), "actions.jsonl")
	closeLog, err := observer.EnableActionLog(config.ActionLog{Enabled: true, Path: path})
	require.NoError(t, err)
	require.NoError(t, observer.actions.file.Close())
	observer.Action(t.Context(), "first", "payload")
	observer.Action(t.Context(), "second", "payload")
	require.Equal(t, 1, strings.Count(logs.String(), "action log disabled after write failure"))
	_ = closeLog()
}
