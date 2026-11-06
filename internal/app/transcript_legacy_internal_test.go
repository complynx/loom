package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
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
			second := repeatedTranscriptImport(t, f)
			handle, err := f.tools.repository.CreateArtifactHandle(ctx, f.p, f.run.ID, f.first.ID)
			require.NoError(t, err)
			args := json.RawMessage(`{"handle_id":"` + handle.ID + `","language":"en","start":null,"end":null}`)
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
			if mode == "unknown" {
				chat, createErr := f.tools.repository.CreateChat(ctx, f.p)
				require.NoError(t, createErr)
				next, createErr := f.tools.repository.CreateRun(ctx, f.p, "new-import",
					domain.CreateRun{ChatID: chat.ID, Input: domain.Input{Text: "Reuse the audio"}})
				require.NoError(t, createErr)
				f.run, createErr = f.tools.repository.ClaimRun(ctx, next.ID, "worker", time.Minute)
				require.NoError(t, createErr)
			}
			newArgs := json.RawMessage(`{"artifact_id":"` + second.ID + `","language":"fr"}`)
			call := provider.Call{ID: "new-call", Name: "audio_transcribe", Arguments: newArgs}
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
				require.Equal(t, sourceUUID(t, f.first.ID), record.SourceArtifactID)
			} else {
				require.Error(t, err)
				require.False(t, found)
				require.Empty(t, output)
			}
			if mode == "unknown" {
				_, key, identityErr := f.tools.audioIdentity(ctx, f.run, f.p,
					audioArguments{ArtifactID: second.ID})
				require.NoError(t, identityErr)
				intent.Call.Arguments = newArgs
				intent.TranscriptSourceID, intent.TranscriptKey = second.ID, key
				intent.ReprocessKey = uuid.NewString()
				input, err = json.Marshal(intent)
				require.NoError(t, err)
				_, err = f.tools.repository.BeginStep(ctx, f.run.ID, f.run.LeaseOwner, f.run.Fence,
					"transcription", input, 75)
				require.ErrorContains(t, err, "transcription_in_progress")
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
