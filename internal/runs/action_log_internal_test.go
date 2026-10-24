package runs

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/provider"
	"github.com/complynx/loom/internal/telemetry"
)

func TestToolJournalPreservesDispatchAndSteering(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"success", "error", "superseded"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "actions.jsonl")
			observer := telemetry.New(slog.New(slog.DiscardHandler))
			closeLog, err := observer.EnableActionLog(config.ActionLog{Enabled: true, Path: path})
			require.NoError(t, err)
			t.Cleanup(func() { _ = closeLog() })
			called := false
			wantedErr := errors.New("private-script-error")
			engine := New(nil, nil, Options{
				ObserveAction: observer.BeginAction,
				Script: func(_ context.Context, _ domain.Run, _ domain.Principal, token, code string) (json.RawMessage, error) {
					called = true
					require.Equal(t, "private-credential", token)
					require.Equal(t, "return 42", code)
					if scenario == "error" {
						return nil, wantedErr
					}
					return json.RawMessage(`{"value":42}`), nil
				},
			})
			run := domain.Run{ID: "run-id", Interaction: domain.Interaction{ReplyMode: domain.ReplyModeAgent}}
			result, waiting, err := engine.dispatchPending(t.Context(), run, domain.Principal{}, "private-credential",
				provider.Call{ID: "call-id", Name: toolScript, Arguments: json.RawMessage(`{"code":"return 42"}`)},
				domain.Settings{}, scenario == "superseded")
			require.False(t, waiting)
			if scenario == "error" {
				require.ErrorIs(t, err, wantedErr)
			} else {
				require.NoError(t, err)
			}
			if scenario == "superseded" {
				require.False(t, called)
				require.JSONEq(t, `{"status":"cancelled","reason":"superseded_by_steering"}`, string(result))
			} else {
				require.True(t, called)
			}
			raw, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			for _, wanted := range []string{"run-id", "call-id", "return 42", "action.started", "action.completed"} {
				require.Contains(t, string(raw), wanted)
			}
			require.NotContains(t, string(raw), "private-credential")
			require.NotContains(t, string(raw), wantedErr.Error())
		})
	}
}
