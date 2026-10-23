package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/provider"
)

func TestLegacyTranscriptUpgradeNeverRegenerates(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"live", "expired", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := referenceFixture(t)
			ctx := t.Context()
			handle, err := f.tools.repository.CreateArtifactHandle(ctx, f.p, f.run.ID, f.first.ID)
			require.NoError(t, err)
			args := json.RawMessage(`{"handle_id":"` + handle.ID + `","language":"en"}`)
			prepared, _ := json.Marshal(preparedAudio{SourceArtifactID: f.first.ID, Language: "en", Start: 0, End: 1})
			intent := domain.PaidToolIntent{
				Call:     domain.ToolCall{ID: "legacy", Name: "audio_transcribe", Arguments: args},
				Prepared: prepared,
				Billing: domain.PaidToolBillingSnapshot{
					Provider:            "openai",
					Model:               "old-model",
					Mode:                "duration",
					DurationMS:          1000,
					CreditsPerSecond:    75,
					ReserveMicrocredits: 75,
				},
			}
			input, _ := json.Marshal(intent)
			step, err := f.tools.repository.BeginStep(
				ctx,
				f.run.ID,
				f.run.LeaseOwner,
				f.run.Fence,
				"transcription",
				input,
				75,
			)
			require.NoError(t, err)
			text := strings.Repeat("old full transcript 猫\n", 3000)
			if mode == "unknown" {
				require.NoError(
					t,
					f.tools.repository.MarkStepUnknown(ctx, f.run.ID, f.run.LeaseOwner, f.run.Fence, step.ID),
				)
			} else {
				artifact, deriveErr := f.tools.files.DeriveToChat(
					ctx,
					f.p,
					f.run.OwnerID,
					f.run.ChatID,
					f.first.ID,
					"text/plain; charset=utf-8",
					strings.NewReader(text),
				)
				require.NoError(t, deriveErr)
				receipt, _ := json.Marshal(
					map[string]any{
						"text":                   "bounded old preview",
						"partial":                true,
						"transcript_artifact_id": artifact.ID,
					},
				)
				require.NoError(
					t,
					f.tools.repository.CompleteStep(
						ctx,
						f.run.ID,
						f.run.LeaseOwner,
						f.run.Fence,
						step.ID,
						provider.CallOutput("legacy", receipt),
						domain.Usage{AudioDurationMS: 1000, Microcredits: 75},
					),
				)
				if mode == "expired" {
					_, err = f.tools.repository.Pool().
						Exec(ctx, "UPDATE artifacts SET expires_at=now()-interval '1 second' WHERE id=$1", artifact.ID)
					require.NoError(t, err)
					_, err = f.tools.repository.SweepLifecycle(ctx, 100)
					require.NoError(t, err)
					_, err = f.tools.files.CollectDeleted(ctx, 100)
					require.NoError(t, err)
				}
			}
			call := provider.Call{ID: "new-call", Name: "audio_transcribe", Arguments: args}
			output, found, err := f.tools.Reuse(ctx, f.run, f.p, "", call)
			if mode == "live" {
				require.NoError(t, err)
				require.True(t, found)
				require.Contains(t, string(output), `"model":"old-model"`)
				record, readErr := f.tools.repository.ReadTranscript(ctx, f.p, step.ID)
				require.NoError(t, readErr)
				var complete struct {
					Text string `json:"text"`
				}
				require.NoError(t, json.Unmarshal(record.Result, &complete))
				require.Equal(t, text, complete.Text)
			} else {
				require.Error(t, err)
				require.False(t, found)
				require.Empty(t, output)
			}
			var count int
			require.NoError(
				t,
				f.tools.repository.Pool().
					QueryRow(ctx, "SELECT count(*) FROM steps WHERE kind='transcription'").
					Scan(&count),
			)
			require.Equal(t, 1, count)
		})
	}
}
