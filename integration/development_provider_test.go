package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/clx-scripter/internal/domain"
	"github.com/complynx/clx-scripter/internal/provider"
	"github.com/complynx/clx-scripter/internal/runs"
)

func TestManualQAProviderUsesProductionAccounting(t *testing.T) {
	t.Parallel()
	for _, invalid := range []bool{false, true} {
		t.Run(fmt.Sprintf("invalid-tool-%t", invalid), func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			settings, err := f.repository.GetSettings(ctx)
			require.NoError(t, err)
			settings.Models[0].InputCachedMicrocreditsPerMillion = 100000
			settings.Models[0].InputCacheWriteMicrocreditsPerMillion = 2000000
			admin := domain.Principal{
				Issuer:      "https://test.identity",
				Subject:     "admin",
				OrgID:       "test-org",
				Permissions: map[string]bool{"settings.write": true},
			}
			_, err = f.repository.UpdateSettings(ctx, admin, settings.Version, settings)
			require.NoError(t, err)
			dir := t.TempDir()
			client, err := provider.NewMock(dir)
			require.NoError(t, err)
			run := f.createRun(t, runRequest("", "", "Help choose"), "manual-qa")
			engine := runs.New(
				f.repository,
				client,
				runs.Options{
					Owner: "manual-worker",
					Authorize: func(context.Context, domain.Run) (domain.Principal, string, error) {
						return domain.Principal{
							Issuer:      "https://test.identity",
							Subject:     "alice",
							Actor:       "alice",
							ExpiresAt:   time.Now().Add(time.Hour),
							OrgID:       "test-org",
							Permissions: map[string]bool{"chat.read": true, "runs.create": true},
						}, "user-token", nil
					},
				},
			)
			done := make(chan error, 1)
			go func() { done <- engine.Execute(ctx, run.ID) }()
			var paths []string
			require.Eventually(
				t,
				func() bool { paths, _ = filepath.Glob(filepath.Join(dir, "*.request.json")); return len(paths) == 1 },
				5*time.Second,
				10*time.Millisecond,
			)
			raw, err := os.ReadFile(paths[0])
			require.NoError(t, err)
			var request provider.Request
			require.NoError(t, json.Unmarshal(raw, &request))
			require.Equal(t, "test-model", request.Model)
			response := provider.Response{
				ID:    "qa-answer",
				Text:  "Choose Andrey",
				Usage: provider.Usage{InputTokens: 20, CachedInputTokens: 5, CacheWriteTokens: 10, OutputTokens: 5},
			}
			if invalid {
				response.Calls = []provider.Call{
					{ID: "bad-call", Name: "missing-tool", Arguments: json.RawMessage(`{}`)},
				}
			}
			raw, err = json.Marshal(response)
			require.NoError(t, err)
			temp := filepath.Join(dir, "answer.tmp")
			require.NoError(t, os.WriteFile(temp, raw, 0600))
			require.NoError(t, os.Rename(temp, paths[0][:len(paths[0])-len(".request.json")]+".response.json"))
			require.NoError(t, <-done)
			result := f.run(t, run.ID)
			if invalid {
				require.Equal(t, domain.Paused, result.State)
				require.Empty(t, result.Result)
			} else {
				require.Equal(t, domain.Completed, result.State)
				require.Equal(t, "Choose Andrey", result.Result)
			}
			require.EqualValues(t, 31, result.SpentMicrocredits)
			require.Zero(t, result.ReservedMicrocredits)
		})
	}
}
