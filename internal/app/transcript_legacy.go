package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/complynx/loom/internal/artifacts"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/store"
	"github.com/complynx/loom/internal/transcription"
)

func (t runtimeTools) archiveLegacyTranscript(
	ctx context.Context,
	r domain.Run,
	p domain.Principal,
	sourceID, key string,
	args audioArguments,
) error {
	parameters, _ := json.Marshal(
		map[string]any{
			"language":    args.Language,
			"prompt":      args.Prompt,
			audioStartKey: args.Start,
			audioEndKey:   args.End,
		},
	)
	record, err := t.repository.LegacyTranscript(ctx, p, sourceID, key, parameters)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	retained, err := t.legacyTranscriptBody(ctx, r, record)
	if err != nil {
		return err
	}
	return t.repository.ArchiveLegacyTranscript(ctx, p, record, key, retained)
}

func (t runtimeTools) legacyTranscriptBody(
	ctx context.Context,
	r domain.Run,
	record store.Transcript,
) (json.RawMessage, error) {
	if record.State != transcriptCompletedState {
		return nil, nil
	}
	var envelope struct {
		Output string `json:"output"`
	}
	var receipt struct {
		ArtifactID string                  `json:"transcript_artifact_id"`
		RequestID  string                  `json:"provider_request_id"`
		Segments   []transcription.Segment `json:"segments"`
	}
	if json.Unmarshal(record.Result, &envelope) != nil || json.Unmarshal([]byte(envelope.Output), &receipt) != nil ||
		receipt.ArtifactID == "" {
		return nil, nil
	}
	_, reader, err := t.files.Download(ctx, artifacts.Principal{OwnerID: r.OwnerID, OrgID: r.OrgID}, receipt.ArtifactID)
	if missingFile(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	text, err := io.ReadAll(io.LimitReader(reader, transcription.MaxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(text)) > transcription.MaxResponseBytes {
		return nil, artifacts.ErrTooLarge
	}
	return json.Marshal(
		transcription.Result{Text: string(text), Segments: receipt.Segments, RequestID: receipt.RequestID},
	)
}
