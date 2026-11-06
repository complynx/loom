package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/provider"
	"github.com/complynx/loom/internal/store"
)

func repeatedTranscriptImport(t *testing.T, f *fileReferenceFixture) domain.Artifact {
	t.Helper()
	raw, err := f.tools.fileBroker(f.run).Call(t.Context(), "fixture", "fetch_file",
		json.RawMessage(`{"external_id":"fixture-file"}`))
	require.NoError(t, err)
	var result struct {
		Artifacts []domain.Artifact `json:"artifacts"`
	}
	require.NoError(t, json.Unmarshal(raw, &result))
	require.Len(t, result.Artifacts, 1)
	second := result.Artifacts[0]
	require.NotEqual(t, f.first.ID, second.ID)
	require.Equal(t, f.first.SHA256, second.SHA256)
	require.Equal(t, f.first.Size, second.Size)
	return second
}

func TestASRRepeatedImportReusesVerifiedContentDespiteHintDrift(t *testing.T) {
	t.Parallel()
	f, workers, calls, _ := transcriptFixture(t)
	second := repeatedTranscriptImport(t, f)
	call := provider.Call{ID: "first", Name: "audio_transcribe",
		Arguments: json.RawMessage(`{"artifact_id":"` + f.first.ID + `"}`)}
	_, original := settleTranscript(t, f, call)
	call.ID = "second"
	call.Arguments = json.RawMessage(`{"artifact_id":"` + second.ID + `","language":"en"}`)
	output, found, err := f.tools.Reuse(t.Context(), f.run, f.p, "", call)
	require.NoError(t, err)
	require.True(t, found, "a new message reference or language hint must not buy the same ASR again")
	var result struct {
		ID     string `json:"transcript_id"`
		Reused bool   `json:"reused"`
	}
	require.NoError(t, json.Unmarshal(output, &result))
	require.Equal(t, original, result.ID)
	require.True(t, result.Reused)
	require.EqualValues(t, 1, workers.Load())
	require.EqualValues(t, 1, calls.Load())
	// Even a caller that missed the lookup cannot reserve another default after completion.
	intent, reserve, err := f.tools.Prepare(t.Context(), f.run, f.p, "", call)
	require.NoError(t, err)
	raw, err := json.Marshal(intent)
	require.NoError(t, err)
	_, err = f.tools.repository.BeginStep(t.Context(), f.run.ID, f.run.LeaseOwner, f.run.Fence,
		"transcription", raw, reserve)
	require.Error(t, err)
	var reservations, settlements int
	require.NoError(t, f.tools.repository.Pool().QueryRow(t.Context(),
		`SELECT count(*) FILTER (WHERE kind='reserve'),count(*) FILTER (WHERE kind='settle')
 FROM credit_ledger`).Scan(&reservations, &settlements))
	require.Equal(t, 1, reservations)
	require.Equal(t, 1, settlements)
}

func TestASRContentReuseKeepsOldDurableReceiptsAndSurvivesGC(t *testing.T) {
	t.Parallel()
	f, workers, calls, _ := transcriptFixture(t)
	ctx := t.Context()
	call := provider.Call{ID: "first", Name: "audio_transcribe",
		Arguments: json.RawMessage(`{"artifact_id":"` + f.first.ID + `","start":null,"end":null}`)}
	_, original := settleTranscript(t, f, call)
	oldKey := restoreSourceKeyedTranscript(t, f, original)
	second := repeatedTranscriptImport(t, f)
	call.Arguments = json.RawMessage(`{"artifact_id":"` + second.ID + `","language":"en","prompt":"hint"}`)
	_, err := f.tools.repository.Pool().Exec(ctx, `UPDATE artifacts SET expires_at=now()-interval '1 second'`)
	require.NoError(t, err)
	_, err = f.tools.repository.SweepLifecycle(ctx, 100)
	require.NoError(t, err)
	_, err = f.tools.files.CollectDeleted(ctx, 100)
	require.NoError(t, err)
	f.tools.repository = store.New(f.tools.repository.Pool(), nil)
	f.tools.cfg.Transcription.Model = "another-model"
	f.changed.Store(true)
	beforeFetches, beforeDownloads := f.fetches.Load(), f.downloads.Load()
	output, found, err := f.tools.Reuse(ctx, f.run, f.p, "", call)
	require.NoError(t, err)
	require.True(t, found)
	require.Contains(t, string(output), original)
	require.Contains(t, string(output), `"model":"fixture-asr"`)
	var retainedKey string
	require.NoError(t, f.tools.repository.Pool().QueryRow(ctx,
		`SELECT processing_key FROM audio_transcripts WHERE id=$1`, original).Scan(&retainedKey))
	require.Equal(t, oldKey, retainedKey)
	require.Equal(t, beforeFetches, f.fetches.Load())
	require.Equal(t, beforeDownloads, f.downloads.Load())
	require.EqualValues(t, 1, workers.Load())
	require.EqualValues(t, 1, calls.Load())
}

func restoreSourceKeyedTranscript(t *testing.T, f *fileReferenceFixture, original string) string {
	t.Helper()
	// Restore the pre-fix processing key, as stored by an already deployed writer.
	oldJSON, err := json.Marshal(struct {
		Source   string  `json:"source"`
		Language string  `json:"language"`
		Prompt   string  `json:"prompt"`
		Start    float64 `json:"start"`
		End      float64 `json:"end"`
	}{sourceUUID(t, f.first.ID), "", "", 0, 0})
	require.NoError(t, err)
	oldHash := sha256.Sum256(oldJSON)
	oldKey := hex.EncodeToString(oldHash[:])
	_, err = f.tools.repository.Pool().Exec(t.Context(),
		`UPDATE audio_transcripts SET processing_key=$2 WHERE id=$1`, original, oldKey)
	require.NoError(t, err)
	_, err = f.tools.repository.Pool().Exec(t.Context(),
		`UPDATE steps SET input=jsonb_set(input,'{transcript_key}',to_jsonb($2::text)) WHERE id=$1`, original, oldKey)
	require.NoError(t, err)
	return oldKey
}

func TestASRContentAdmissionFencesCompletedSourceKeyedReceipt(t *testing.T) {
	t.Parallel()
	f, _, calls, _ := transcriptFixture(t)
	ctx := t.Context()
	second := repeatedTranscriptImport(t, f)
	call := provider.Call{ID: "first", Name: "audio_transcribe",
		Arguments: json.RawMessage(`{"artifact_id":"` + f.first.ID + `","start":null,"end":null}`)}
	_, tid := settleTranscript(t, f, call)
	oldKey := restoreSourceKeyedTranscript(t, f, tid)
	var before string
	require.NoError(t, f.tools.repository.Pool().QueryRow(ctx,
		`SELECT input::text FROM steps WHERE id=$1`, tid).Scan(&before))
	// Simulate an earlier lookup miss followed by completion while the new import was being prepared.
	call.Arguments = json.RawMessage(`{"artifact_id":"` + second.ID + `","language":"en"}`)
	intent, reserve, err := f.tools.Prepare(ctx, f.run, f.p, "", call)
	require.NoError(t, err)
	require.NotEqual(t, oldKey, intent.TranscriptKey, "old unique key alone cannot fence this admission")
	raw, err := json.Marshal(intent)
	require.NoError(t, err)
	_, err = f.tools.repository.BeginStep(ctx, f.run.ID, f.run.LeaseOwner, f.run.Fence,
		"transcription", raw, reserve)
	require.ErrorContains(t, err, "transcription_in_progress")
	var after string
	var reservations int
	require.NoError(t, f.tools.repository.Pool().QueryRow(ctx,
		`SELECT input::text FROM steps WHERE id=$1`, tid).Scan(&after))
	require.Equal(t, before, after)
	require.NoError(t, f.tools.repository.Pool().QueryRow(ctx,
		`SELECT count(*) FROM credit_ledger WHERE kind='reserve'`).Scan(&reservations))
	require.Equal(t, 1, reservations)
	require.EqualValues(t, 1, calls.Load())
}

func sourceUUID(t *testing.T, raw string) string {
	t.Helper()
	id, err := uuid.Parse(raw)
	require.NoError(t, err)
	return id.String()
}

func TestASRContentReprocessUUIDBindsParametersAndRange(t *testing.T) {
	t.Parallel()
	f, _, calls, _ := transcriptFixture(t)
	second := repeatedTranscriptImport(t, f)
	key := uuid.NewString()
	call := provider.Call{ID: "first", Name: "audio_transcribe", Arguments: json.RawMessage(
		`{"artifact_id":"` + f.first.ID + `","language":"en","reprocess_key":"` + key + `"}`)}
	_, original := settleTranscript(t, f, call)
	call.Arguments = json.RawMessage(`{"artifact_id":"` + second.ID +
		`","language":"en","reprocess_key":"` + key + `"}`)
	output, found, err := f.tools.Reuse(t.Context(), f.run, f.p, "", call)
	require.NoError(t, err)
	require.True(t, found)
	require.Contains(t, string(output), original)
	call.Arguments = json.RawMessage(`{"artifact_id":"` + second.ID +
		`","language":"ru","reprocess_key":"` + key + `"}`)
	_, _, err = f.tools.Reuse(t.Context(), f.run, f.p, "", call)
	require.ErrorContains(t, err, "transcript_reprocess_parameters_changed")
	_, base, err := f.tools.audioIdentity(t.Context(), f.run, f.p, audioArguments{ArtifactID: second.ID})
	require.NoError(t, err)
	_, chunk, err := f.tools.audioIdentity(t.Context(), f.run, f.p,
		audioArguments{ArtifactID: second.ID, Start: 1, End: 2})
	require.NoError(t, err)
	require.NotEqual(t, base, chunk)
	require.EqualValues(t, 1, calls.Load())

	// A default, a new explicit operation and a different range each retain their own result.
	call.ID = "default"
	call.Arguments = json.RawMessage(`{"artifact_id":"` + second.ID + `"}`)
	_, defaultID := settleTranscript(t, f, call)
	require.NotEqual(t, original, defaultID)
	call.ID = "explicit"
	call.Arguments = json.RawMessage(`{"artifact_id":"` + second.ID +
		`","language":"ru","reprocess_key":"` + uuid.NewString() + `"}`)
	_, explicitID := settleTranscript(t, f, call)
	require.NotEqual(t, defaultID, explicitID)
	call.ID = "chunk"
	call.Arguments = json.RawMessage(`{"artifact_id":"` + second.ID + `","start":1,"end":2}`)
	_, chunkID := settleTranscript(t, f, call)
	require.NotEqual(t, defaultID, chunkID)
	call.Arguments = json.RawMessage(`{"artifact_id":"` + second.ID + `","language":"fr"}`)
	output, found, err = f.tools.Reuse(t.Context(), f.run, f.p, "", call)
	require.NoError(t, err)
	require.True(t, found)
	require.Contains(t, string(output), defaultID)
	require.EqualValues(t, 4, calls.Load())
	var reservations, settlements int
	require.NoError(t, f.tools.repository.Pool().QueryRow(t.Context(),
		`SELECT count(*) FILTER (WHERE kind='reserve'),count(*) FILTER (WHERE kind='settle')
 FROM credit_ledger`).Scan(&reservations, &settlements))
	require.Equal(t, 4, reservations)
	require.Equal(t, 4, settlements)
}

func TestASRContentReuseDeniesDeletedOriginalAndForeignOwner(t *testing.T) {
	t.Parallel()
	f, _, calls, _ := transcriptFixture(t)
	ctx := t.Context()
	call := provider.Call{ID: "first", Name: "audio_transcribe",
		Arguments: json.RawMessage(`{"artifact_id":"` + f.first.ID + `"}`)}
	_, tid := settleTranscript(t, f, call)
	firstRun := f.run
	require.NoError(t, f.tools.repository.AttachChatArtifact(ctx, f.p, firstRun.ChatID, f.first.ID))
	require.NoError(t, f.tools.repository.Transition(ctx, firstRun.ID, firstRun.LeaseOwner,
		firstRun.Fence, domain.Completed, "", "done"))
	chat, err := f.tools.repository.CreateChat(ctx, f.p)
	require.NoError(t, err)
	next, err := f.tools.repository.CreateRun(ctx, f.p, "next",
		domain.CreateRun{ChatID: chat.ID, Input: domain.Input{Text: "Reuse the same audio"}})
	require.NoError(t, err)
	f.run, err = f.tools.repository.ClaimRun(ctx, next.ID, "worker", time.Minute)
	require.NoError(t, err)
	second := repeatedTranscriptImport(t, f)
	call.Arguments = json.RawMessage(`{"artifact_id":"` + second.ID + `"}`)
	_, found, err := f.tools.Reuse(ctx, f.run, f.p, "", call)
	require.NoError(t, err)
	require.True(t, found)
	foreign := f.p
	foreign.Subject, foreign.Actor = "another-owner", "another-owner"
	_, err = f.tools.repository.ResolveUser(ctx, foreign)
	require.NoError(t, err)
	_, key, err := f.tools.audioIdentity(ctx, f.run, f.p, audioArguments{ArtifactID: second.ID})
	require.NoError(t, err)
	_, err = f.tools.repository.FindContentTranscript(ctx, foreign, key, "", second.ID,
		json.RawMessage(`{"start":0,"end":0}`))
	require.ErrorIs(t, err, domain.ErrNotFound)
	_, err = f.tools.repository.Pool().Exec(ctx,
		`UPDATE chats SET deleted_at=now() WHERE id=$1`, firstRun.ChatID)
	require.NoError(t, err)
	_, _, err = f.tools.Reuse(ctx, f.run, f.p, "", call)
	require.Error(t, err)
	_, err = f.tools.readTranscript(ctx, f.run,
		json.RawMessage(`{"transcript_id":"`+tid+`"}`))
	require.Error(t, err)
	require.EqualValues(t, 1, calls.Load())
	delete(f.p.Permissions, "qa.read")
	_, _, err = f.tools.Reuse(ctx, f.run, f.p, "", call)
	require.Error(t, err)
	require.EqualValues(t, 1, calls.Load())
}
