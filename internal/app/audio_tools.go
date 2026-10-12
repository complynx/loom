package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/complynx/loom/internal/artifacts"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/fileproc"
	"github.com/complynx/loom/internal/provider"
	"github.com/complynx/loom/internal/runs"
	"github.com/complynx/loom/internal/transcription"
)

const (
	audioMillisecondsPerSecond = 1000
	maxTranscriptSegments      = 100
)

type audioArguments struct {
	HandleID string  `json:"handle_id"`
	Language string  `json:"language"`
	Prompt   string  `json:"prompt"`
	Start    float64 `json:"start"`
	End      float64 `json:"end"`
}

type preparedAudio struct {
	SourceArtifactID string  `json:"source_artifact_id"`
	HandleID         string  `json:"handle_id"`
	Language         string  `json:"language"`
	Prompt           string  `json:"prompt"`
	Start            float64 `json:"start"`
	End              float64 `json:"end"`
	NextCursor       string  `json:"next_cursor,omitempty"`
}

func transcriptionTool() provider.Tool {
	return provider.Tool{
		Type:        "function",
		Name:        "audio_transcribe",
		Description: "Transcribe audio/video from temporary handle outside JS. Paid durable action; credits reserved before provider dispatch, no automatic retry. Optional start/end choose a <=300-second audio chunk, language and short prompt improve terms. Returns bounded transcript, full transcript handle, provider timestamps only if available and next_cursor for following chunks. Use files.video_storyboard separately for visual frames.",
		Strict:      false,
		Parameters: json.RawMessage(
			`{"type":"object","properties":{"handle_id":{"type":"string"},"language":{"type":"string"},"prompt":{"type":"string"},"start":{"type":"number"},"end":{"type":"number"}},"required":["handle_id"],"additionalProperties":false}`,
		),
	}
}

func (t runtimeTools) Prepare(
	ctx context.Context,
	r domain.Run,
	_ domain.Principal,
	_ string,
	call provider.Call,
) (domain.PaidToolIntent, int64, error) {
	if !t.cfg.Transcription.Enabled || t.transcriber == nil {
		return domain.PaidToolIntent{}, 0, domain.Forbidden()
	}
	current, _, err := t.fresh(ctx, r)
	if err != nil {
		return domain.PaidToolIntent{}, 0, err
	}
	var args audioArguments
	if err = decodeAudio(call.Arguments, &args); err != nil {
		return domain.PaidToolIntent{}, 0, err
	}
	if args.HandleID == "" || len(args.Language) > 32 || len(args.Prompt) > 1000 || args.Start < 0 || args.End < 0 ||
		math.IsNaN(args.Start) ||
		math.IsNaN(args.End) ||
		math.IsInf(args.Start, 0) ||
		math.IsInf(args.End, 0) {
		return domain.PaidToolIntent{}, 0, artifacts.ErrInvalid
	}
	source, err := t.fileSource(ctx, r, current, args.HandleID)
	if err != nil {
		return domain.PaidToolIntent{}, 0, err
	}
	options, _ := json.Marshal(fileproc.VideoOptions{Start: args.Start, End: args.End})
	audio, err := t.processSource(ctx, r, current, source, fileproc.Request{Operation: "video.audio", Params: options})
	if err != nil {
		return domain.PaidToolIntent{}, 0, err
	}
	var bounds struct {
		HasAudio bool    `json:"has_audio"`
		Start    float64 `json:"start"`
		End      float64 `json:"end"`
	}
	if err = json.Unmarshal(
		audio.Data,
		&bounds,
	); err != nil || !bounds.HasAudio || len(audio.Outputs) != 1 || bounds.End <= bounds.Start ||
		bounds.End-bounds.Start > float64(t.cfg.Transcription.MaxDurationSeconds) {
		return domain.PaidToolIntent{}, 0, artifacts.ErrInvalid
	}
	duration := int64(math.Ceil((bounds.End-bounds.Start)*audioMillisecondsPerSecond)) + audioMillisecondsPerSecond
	billing := domain.PaidToolBillingSnapshot{
		Provider:         t.cfg.Transcription.Provider,
		Model:            t.cfg.Transcription.Model,
		Mode:             "duration",
		DurationMS:       duration,
		CreditsPerSecond: t.cfg.Transcription.CreditsPerSecond,
	}
	reserve, err := domain.PaidToolCost(domain.Usage{AudioDurationMS: duration}, billing)
	if err != nil {
		return domain.PaidToolIntent{}, 0, err
	}
	billing.ReserveMicrocredits = reserve
	prepared, _ := json.Marshal(
		preparedAudio{
			SourceArtifactID: source.ID,
			HandleID:         audio.Outputs[0].HandleID,
			Language:         args.Language,
			Prompt:           args.Prompt,
			Start:            bounds.Start,
			End:              bounds.End,
			NextCursor:       audio.NextCursor,
		},
	)
	return domain.PaidToolIntent{
		Call:     domain.ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments},
		Billing:  billing,
		Prepared: prepared,
	}, reserve, nil
}

func decodeAudio(raw json.RawMessage, target *audioArguments) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return artifacts.ErrInvalid
	}
	return nil
}

func (t runtimeTools) Execute(
	ctx context.Context,
	r domain.Run,
	_ domain.Principal,
	_ string,
	intent domain.PaidToolIntent,
	stepID string,
) (json.RawMessage, domain.Usage, error) {
	current, _, err := t.fresh(ctx, r)
	if err != nil {
		return nil, domain.Usage{}, notDispatched(err)
	}
	var prepared preparedAudio
	if err = json.Unmarshal(intent.Prepared, &prepared); err != nil {
		return nil, domain.Usage{}, notDispatched(err)
	}
	if err = t.authorizeAudioSource(ctx, r, prepared); err != nil {
		return nil, domain.Usage{}, notDispatched(err)
	}
	source, err := t.fileSource(ctx, r, current, prepared.HandleID)
	if err != nil {
		return nil, domain.Usage{}, notDispatched(err)
	}
	if err = t.bridge.guard(ctx, r); err != nil {
		return nil, domain.Usage{}, notDispatched(err)
	}
	_, audio, err := t.files.Download(ctx, artifacts.Principal{OwnerID: r.OwnerID, OrgID: r.OrgID}, source.ID)
	if err != nil {
		return nil, domain.Usage{}, notDispatched(err)
	}
	defer func() { _ = audio.Close() }()
	if t.observer != nil {
		var finishTrace func(error)
		ctx, finishTrace = t.observer.Start(ctx, "transcription", r.ID)
		defer func() { finishTrace(err) }()
	}
	started := time.Now()
	result, err := t.transcriber.Transcribe(
		ctx,
		transcription.Request{
			Model:           intent.Billing.Model,
			ClientRequestID: stepID,
			Filename:        "audio.wav",
			MIME:            "audio/wav",
			Audio:           audio,
			Language:        prepared.Language,
			Prompt:          prepared.Prompt,
			DurationSeconds: prepared.End - prepared.Start,
		},
	)
	if t.observer != nil {
		outcome := operationOutcome(err)
		t.observer.Operation(ctx, "transcription", outcome, r.ID, started)
	}
	if err != nil {
		return nil, domain.Usage{}, paidTranscriptionError(err)
	}
	if result.Usage.Type != transcription.UsageDuration || result.Usage.DurationSeconds <= 0 ||
		math.IsNaN(result.Usage.DurationSeconds) ||
		math.IsInf(result.Usage.DurationSeconds, 0) ||
		result.Usage.DurationSeconds > float64(intent.Billing.DurationMS)/1000 {
		return nil, domain.Usage{}, errors.New("uncertain transcription duration receipt")
	}
	durationMS, durationErr := transcription.DurationMilliseconds(result.Usage.DurationSeconds)
	if durationErr != nil {
		return nil, domain.Usage{}, durationErr
	}
	usage := domain.Usage{
		AudioDurationMS: durationMS,
		RequestID:       result.RequestID,
	}
	output, err := t.transcriptOutput(ctx, r, prepared, source, result)
	return output, usage, err
}

func notDispatched(err error) error {
	return &runs.PaidToolError{Disposition: "not_dispatched", Cause: err}
}
func paidTranscriptionError(err error) error {
	if failure, ok := errors.AsType[*transcription.FailureError](err); ok {
		switch failure.Kind {
		case transcription.FailurePreDispatch:
			return notDispatched(err)
		case transcription.FailureRejected:
			return &runs.PaidToolError{Disposition: runs.PaidToolRejected, Cause: err}
		}
	}
	return err
}

func (t runtimeTools) transcriptOutput(
	ctx context.Context,
	r domain.Run,
	prepared preparedAudio,
	source domain.Artifact,
	result transcription.Result,
) (json.RawMessage, error) {
	current, _, err := t.fresh(ctx, r)
	if err != nil {
		return nil, err
	}
	if err = t.authorizeAudioSource(ctx, r, prepared); err != nil {
		return nil, err
	}
	if _, err = t.fileSource(ctx, r, current, prepared.HandleID); err != nil {
		return nil, err
	}
	transcript, err := t.files.DeriveToChat(
		ctx,
		current,
		r.OwnerID,
		r.ChatID,
		source.ID,
		"text/plain; charset=utf-8",
		strings.NewReader(result.Text),
	)
	if err != nil {
		return nil, err
	}
	handle, err := t.repository.CreateArtifactHandle(ctx, current, r.ID, transcript.ID)
	if err != nil {
		return nil, err
	}
	text := result.Text
	partial := false
	if len(text) > 32<<10 {
		text = text[:32<<10]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
		partial = true
	}
	segments := result.Segments
	if len(segments) > maxTranscriptSegments {
		segments = segments[:maxTranscriptSegments]
		partial = true
	}
	segmentBytes, marshalErr := json.Marshal(segments)
	if marshalErr != nil {
		return nil, marshalErr
	}
	if len(segmentBytes) > 32<<10 {
		segments = nil
		partial = true
	}
	return json.Marshal(
		map[string]any{
			"text":                   text,
			"partial":                partial,
			"transcript_handle_id":   handle.ID,
			"segments":               segments,
			"transcript_artifact_id": transcript.ID,
			"start":                  prepared.Start,
			"end":                    prepared.End,
			"next_cursor":            prepared.NextCursor,
			"provider_request_id":    result.RequestID,
		},
	)
}

func (t runtimeTools) replayPaidTool(ctx context.Context, r domain.Run, intent domain.PaidToolIntent) error {
	var prepared preparedAudio
	if err := json.Unmarshal(intent.Prepared, &prepared); err != nil {
		return err
	}
	return t.authorizeAudioSource(ctx, r, prepared)
}
func (t runtimeTools) authorizeAudioSource(ctx context.Context, r domain.Run, prepared preparedAudio) error {
	p, _, err := t.fresh(ctx, r)
	if err != nil {
		return err
	}
	if !p.Has("artifacts.read") || !p.Has("chat.read") {
		return domain.Forbidden()
	}
	source, err := t.resolveArtifact(ctx, r, p, prepared.SourceArtifactID)
	if err != nil {
		return err
	}
	settings, err := t.repository.GetSettings(ctx)
	if err != nil {
		return err
	}
	if !settings.ExternalDisclosure || !r.SettingsSnapshot.ExternalDisclosure || !source.ExternalDisclosureAllowed {
		return artifacts.ErrForbidden
	}
	return nil
}
