package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/complynx/loom/internal/artifacts"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/identity"
	"github.com/complynx/loom/internal/provider"
	"github.com/complynx/loom/internal/runs"
	"github.com/complynx/loom/internal/store"
	"github.com/complynx/loom/internal/transcription"
)

const transcriptTextKey = "text"
const transcriptPartialKey = "partial"
const maxTranscriptPreviewBytes = 32 << 10
const transcriptCompletedState = "completed"
const audioStartKey = "start"
const audioEndKey = "end"

func (t runtimeTools) finishTranscript(
	ctx context.Context,
	r domain.Run,
	intent domain.PaidToolIntent,
	prepared preparedAudio,
	source domain.Artifact,
	result transcription.Result,
	sid string,
) (json.RawMessage, error) {
	if intent.TranscriptKey == "" {
		return t.transcriptOutput(ctx, r, prepared, source, result)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if err = t.repository.SaveTranscript(saveCtx, r, sid, raw); err != nil {
		return nil, err
	}
	if err = t.authorizeAudioSource(ctx, r, prepared); err != nil {
		return nil, err
	}
	input, _ := json.Marshal(intent)
	return durableTranscriptOutput(
		store.Transcript{ID: sid, SourceArtifactID: prepared.SourceArtifactID, Result: raw, Intent: input},
		false,
	)
}

func (t runtimeTools) audioIdentity(
	ctx context.Context,
	r domain.Run,
	p domain.Principal,
	args audioArguments,
) (domain.Artifact, string, error) {
	if (args.HandleID == "") == (args.ArtifactID == "") {
		return domain.Artifact{}, "", artifacts.ErrInvalid
	}
	aid := args.ArtifactID
	if aid == "" {
		var err error
		aid, err = t.repository.FileHandleReference(ctx, p, r.ID, args.HandleID)
		if err != nil {
			return domain.Artifact{}, "", err
		}
	}
	source, err := t.authorizeTranscriptSource(ctx, r, p, aid)
	if err != nil {
		return source, "", err
	}
	if args.ReprocessKey != "" {
		if _, err = uuid.Parse(args.ReprocessKey); err != nil {
			return source, "", artifacts.ErrInvalid
		}
	}
	raw, _ := json.Marshal(struct {
		Source   string  `json:"source"`
		Language string  `json:"language"`
		Prompt   string  `json:"prompt"`
		Start    float64 `json:"start"`
		End      float64 `json:"end"`
	}{source.ID, args.Language, args.Prompt, args.Start, args.End})
	hash := sha256.Sum256(raw)
	return source, hex.EncodeToString(hash[:]), nil
}

func (t runtimeTools) authorizeTranscriptSource(
	ctx context.Context,
	r domain.Run,
	p domain.Principal,
	aid string,
) (domain.Artifact, error) {
	source, err := t.repository.TranscriptionSource(ctx, p, r.ID, aid)
	if err != nil {
		return source, err
	}
	if source.SourceModule != "" {
		if err = t.authorizeFileOrigin(ctx, r, source); err != nil {
			return source, err
		}
	}
	settings, err := t.repository.GetSettings(ctx)
	if err != nil {
		return source, err
	}
	if !settings.ExternalDisclosure || !r.SettingsSnapshot.ExternalDisclosure || !source.ExternalDisclosureAllowed {
		return source, artifacts.ErrForbidden
	}
	return source, nil
}

// Reuse checks durable text before file refresh, worker extraction or billing admission.
func (t runtimeTools) Reuse(
	ctx context.Context,
	r domain.Run,
	_ domain.Principal,
	_ string,
	call provider.Call,
) (json.RawMessage, bool, error) {
	ctx = identity.RequireFresh(ctx)
	p, _, err := t.fresh(ctx, r)
	if err != nil {
		return nil, false, err
	}
	var args audioArguments
	if err = decodeAudio(call.Arguments, &args); err != nil {
		return nil, false, err
	}
	if err = validateAudioArguments(args); err != nil {
		return nil, false, err
	}
	source, key, err := t.audioIdentity(ctx, r, p, args)
	if err != nil {
		return nil, false, err
	}
	record, err := t.repository.FindTranscript(ctx, p, key, args.ReprocessKey)
	if errors.Is(err, domain.ErrNotFound) {
		if err = t.archiveLegacyTranscript(ctx, r, p, source.ID, key, args); err != nil {
			return nil, false, err
		}
		record, err = t.repository.FindTranscript(ctx, p, key, args.ReprocessKey)
		if errors.Is(err, domain.ErrNotFound) {
			if args.ReuseOnly {
				return json.RawMessage(`{"error":"transcript_not_found"}`), true, nil
			}
			return nil, false, nil
		}
	}
	if err != nil {
		return nil, false, err
	}
	if record.State != transcriptCompletedState {
		return nil, false, &runs.Suspension{State: domain.Paused, Reason: "transcription_in_progress"}
	}
	if len(record.Result) == 0 {
		return nil, false, &runs.Suspension{State: domain.Paused, Reason: "transcript_result_unavailable"}
	}
	output, err := durableTranscriptOutput(record, true)
	return output, true, err
}

func durableTranscriptOutput(record store.Transcript, reused bool) (json.RawMessage, error) {
	var result transcription.Result
	var intent domain.PaidToolIntent
	var prepared preparedAudio
	if err := json.Unmarshal(record.Result, &result); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(record.Intent, &intent); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(intent.Prepared, &prepared); err != nil {
		return nil, err
	}
	text := result.Text
	partial := len(text) > maxTranscriptPreviewBytes
	if partial {
		text = text[:maxTranscriptPreviewBytes]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	segments := result.Segments
	if len(segments) > maxTranscriptSegments {
		segments = segments[:maxTranscriptSegments]
		partial = true
	}
	raw, err := json.Marshal(segments)
	if err != nil {
		return nil, err
	}
	if len(raw) > 32<<10 {
		segments = nil
		partial = true
	}
	return json.Marshal(
		map[string]any{
			transcriptTextKey:     text,
			transcriptPartialKey:  partial,
			"segments":            segments,
			"transcript_id":       record.ID,
			"source_artifact_id":  record.SourceArtifactID,
			audioStartKey:         prepared.Start,
			audioEndKey:           prepared.End,
			"next_cursor":         prepared.NextCursor,
			"provider_request_id": result.RequestID,
			"provider":            intent.Billing.Provider,
			"model":               intent.Billing.Model,
			"reused":              reused,
		},
	)
}

func (t runtimeTools) readTranscript(ctx context.Context, r domain.Run, raw json.RawMessage) (json.RawMessage, error) {
	ctx = identity.RequireFresh(ctx)
	p, _, err := t.fresh(ctx, r)
	if err != nil {
		return nil, err
	}
	var args struct {
		ID     string `json:"transcript_id"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err = json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	if args.Offset < 0 || args.Limit < 0 || args.Limit > 32768 {
		return nil, artifacts.ErrInvalid
	}
	if args.Limit == 0 {
		args.Limit = 8192
	}
	record, err := t.repository.ReadTranscript(ctx, p, args.ID)
	if err != nil {
		return nil, err
	}
	if _, err = t.authorizeTranscriptSource(ctx, r, p, record.SourceArtifactID); err != nil {
		return nil, err
	}
	if record.State != transcriptCompletedState || len(record.Result) == 0 {
		return nil, domain.NotFound()
	}
	var result transcription.Result
	if err = json.Unmarshal(record.Result, &result); err != nil {
		return nil, err
	}
	if args.Offset > len(result.Text) || (args.Offset < len(result.Text) && !utf8.RuneStart(result.Text[args.Offset])) {
		return nil, artifacts.ErrInvalid
	}
	end := min(len(result.Text), args.Offset+args.Limit)
	for end > args.Offset && !utf8.ValidString(result.Text[args.Offset:end]) {
		end--
	}
	if end == args.Offset && end < len(result.Text) {
		return nil, artifacts.ErrInvalid
	}
	return json.Marshal(
		map[string]any{
			transcriptTextKey:    result.Text[args.Offset:end],
			"offset":             args.Offset,
			"next_offset":        end,
			transcriptPartialKey: end < len(result.Text),
			"transcript_id":      record.ID,
		},
	)
}
