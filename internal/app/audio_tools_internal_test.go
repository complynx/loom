package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/artifacts"
	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/fileimage"
	"github.com/complynx/loom/internal/fileproc"
	"github.com/complynx/loom/internal/filetext"
	"github.com/complynx/loom/internal/modules"
	"github.com/complynx/loom/internal/provider"
	"github.com/complynx/loom/internal/runs"
	"github.com/complynx/loom/internal/sandbox"
	"github.com/complynx/loom/internal/transcription"
)

func TestAudioArgumentsRejectTrailingOrUnknownData(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`{"handle_id":"h"}{}`, `{"handle_id":"h","secret":"hidden"}`, `{"handle_id":"h"} trailing`} {
		var args audioArguments
		require.Error(t, decodeAudio(json.RawMessage(raw), &args))
	}
	var args audioArguments
	require.NoError(t, decodeAudio(json.RawMessage(`{"handle_id":"h","language":"ru"}`), &args))
	require.Equal(t, "h", args.HandleID)
}

func TestFileToolsStreamDerivedHandlesAndBoundTranscript(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("private file worker uses Linux Unix sockets")
	}
	repository, _ := searchToolStore(t)
	ctx := t.Context()
	p := domain.Principal{
		Issuer:      "issuer",
		Subject:     "alice",
		Actor:       "alice",
		OrgID:       "org",
		ExpiresAt:   time.Now().Add(time.Hour),
		Permissions: map[string]bool{"chat.read": true, "runs.create": true, "artifacts.read": true},
	}
	uid, err := repository.ResolveUser(ctx, p)
	require.NoError(t, err)
	files, err := artifacts.New(t.TempDir(), repository, artifacts.Limits{MaxUploadBytes: 2 << 20, QuotaBytes: 8 << 20})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, files.Close()) })
	run, err := repository.CreateRun(ctx, p, "file-run", domain.CreateRun{Input: domain.Input{Text: "read files"}})
	require.NoError(t, err)
	run, err = repository.ClaimRun(ctx, run.ID, "file-worker", time.Minute)
	require.NoError(t, err)
	socket := filepath.Join(t.TempDir(), "files.sock")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	handler := &fileproc.Handler{
		LocalProcessor: func(c context.Context, job fileproc.Request, input, output string) (fileproc.Result, error) {
			var result fileproc.Result
			if strings.HasPrefix(job.Operation, "video.") {
				return fileproc.Process(c, job, input, output)
			}

			if strings.HasPrefix(job.Operation, "image.") {
				data, outputs, partial, next, processErr := fileimage.Process(
					c,
					job.Operation,
					job.Params,
					input,
					output,
				)
				result.Data, result.Partial, result.NextCursor = data, partial, next
				for _, out := range outputs {
					result.Outputs = append(
						result.Outputs,
						fileproc.Output{Name: out.Name, MIME: out.MIME, Path: out.Path},
					)
				}
				return result, processErr
			}
			data, outputs, partial, next, processErr := filetext.Process(c, job.Operation, job.Params, input, output)
			result.Data, result.Partial, result.NextCursor = data, partial, next
			for _, out := range outputs {
				result.Outputs = append(result.Outputs, fileproc.Output{Name: out.Name, MIME: out.MIME, Path: out.Path})
			}
			return result, processErr
		},
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() { require.NoError(t, server.Close()); require.ErrorIs(t, <-done, http.ErrServerClosed) })
	authorize := func(context.Context, domain.Run) (domain.Principal, string, error) { return p, "", nil }
	tools := runtimeTools{
		repository: repository,
		files:      files,
		cfg:        config.Config{FileTools: config.FileTools{Enabled: true, Socket: socket, TimeoutMS: 5000}},
		authorize:  authorize,
		bridge:     NewModuleBridge(nil, nil, repository, authorize),
	}
	upload := func(mime string, data []byte) domain.ArtifactHandle {
		blob, uploadErr := files.Upload(
			ctx,
			artifacts.Principal{OwnerID: uid, OrgID: p.OrgID},
			mime,
			bytes.NewReader(data),
		)
		require.NoError(t, uploadErr)
		handle, handleErr := repository.CreateArtifactHandle(ctx, p, run.ID, blob.ID)
		require.NoError(t, handleErr)
		return handle
	}
	sourceText := strings.Repeat("line one\n", 5000)
	textHandle := upload("text/plain", []byte(sourceText))
	args, _ := json.Marshal(map[string]any{"handle_id": textHandle.ID, "limit": 100})
	raw, err := tools.processFile(ctx, run, p, "text_read", args)
	require.NoError(t, err)
	var textPage fileResult
	require.NoError(t, json.Unmarshal(raw, &textPage))
	require.True(t, textPage.Partial)
	require.NotEmpty(t, textPage.NextCursor)
	require.Contains(t, string(textPage.Data), "line one")
	require.Less(t, len(raw), 1000)
	tools.registry = &modules.Registry{}
	tools.executor = sandbox.Inline{}
	tools.cfg.Sandbox = config.Sandbox{ScriptTimeoutCeilingMS: 5000, ParallelRPCCeiling: 4}
	scriptOutput, scriptErr := tools.executeScript(
		ctx,
		run,
		`return await modules.files.text_read({handle_id:"`+textHandle.ID+`",limit:100});`,
	)
	require.NoError(t, scriptErr)
	require.Contains(t, string(scriptOutput), "line one")
	require.Less(t, len(scriptOutput), 1000)

	var imageBytes bytes.Buffer
	require.NoError(t, png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 20, 10))))
	imageHandle := upload("image/png", imageBytes.Bytes())
	args, _ = json.Marshal(map[string]any{"handle_id": imageHandle.ID, "max_edge": 10})
	raw, err = tools.processFile(ctx, run, p, "image_resize", args)
	require.NoError(t, err)
	var resized fileResult
	require.NoError(t, json.Unmarshal(raw, &resized))
	require.Len(t, resized.Outputs, 1)
	require.NotContains(t, string(raw), "base64")
	require.NotContains(t, string(raw), "/tmp/")
	output := resized.Outputs[0]
	h, err := repository.LoadArtifactHandle(ctx, p, output.HandleID)
	require.NoError(t, err)
	_, stream, err := files.Download(ctx, artifacts.Principal{OwnerID: uid, OrgID: p.OrgID}, h.ArtifactID)
	require.NoError(t, err)
	dimensions, _, err := image.DecodeConfig(stream)
	require.NoError(t, err)
	require.NoError(t, stream.Close())
	require.Equal(t, 10, dimensions.Width)
	require.Equal(t, 5, dimensions.Height)
	foreign := p
	foreign.Subject, foreign.Actor = "bob", "bob"
	_, err = repository.ResolveUser(ctx, foreign)
	require.NoError(t, err)
	_, err = tools.processFile(ctx, run, foreign, "image_info", args)
	require.Error(t, err)
	textSource, err := files.Metadata(ctx, artifacts.Principal{OwnerID: uid, OrgID: p.OrgID}, textHandle.ArtifactID)
	require.NoError(t, err)
	transcript := strings.Repeat("hello\n\"quoted\"\t", 2200) + strings.Repeat("猫", 2000)
	segments := make([]transcription.Segment, 120)
	raw, err = tools.transcriptOutput(
		ctx,
		run,
		preparedAudio{HandleID: textHandle.ID, SourceArtifactID: textSource.ID},
		textSource,
		transcription.Result{Text: transcript, Segments: segments},
	)
	require.NoError(t, err)
	var receipt struct {
		Text     string                  `json:"text"`
		Partial  bool                    `json:"partial"`
		HandleID string                  `json:"transcript_handle_id"`
		Segments []transcription.Segment `json:"segments"`
	}
	require.NoError(t, json.Unmarshal(raw, &receipt))
	require.True(t, receipt.Partial)
	require.True(t, utf8.ValidString(receipt.Text))
	require.LessOrEqual(t, len(receipt.Text), 32<<10)
	require.NotEmpty(t, receipt.Text)
	require.True(t, strings.HasPrefix(transcript, receipt.Text))
	require.Len(t, receipt.Segments, 100)
	transcriptHandle, err := repository.LoadArtifactHandle(ctx, p, receipt.HandleID)
	require.NoError(t, err)
	_, stream, err = files.Download(ctx, artifacts.Principal{OwnerID: uid, OrgID: p.OrgID}, transcriptHandle.ArtifactID)
	require.NoError(t, err)
	complete, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.NoError(t, stream.Close())
	require.Equal(t, transcript, string(complete))
	audioRun := run
	t.Run("production-audio-adapter", func(t *testing.T) {
		t.Parallel()
		exerciseAudioAdapter(t, tools, audioRun, p, uid)
	})
	run.SettingsSnapshot.ExternalDisclosure = false
	_, err = tools.processFile(ctx, run, p, "text_read", json.RawMessage(`{"handle_id":"`+textHandle.ID+`"}`))
	require.ErrorIs(t, err, artifacts.ErrForbidden)
}

func exerciseAudioAdapter(t *testing.T, tools runtimeTools, run domain.Run, p domain.Principal, uid string) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("FFmpeg CLI required for audio extraction")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("FFprobe CLI required for audio extraction")
	}
	ctx := t.Context()
	var uploaded atomic.Int64
	var requests atomic.Int64
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		assert.Equal(t, "/v1/audio/transcriptions", r.URL.Path)
		assert.Equal(t, "Bearer fixture-only-key", r.Header.Get("Authorization"))
		assert.NotEmpty(t, r.Header.Get("X-Client-Request-Id"))
		if !assert.NoError(t, r.ParseMultipartForm(1<<20)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer r.MultipartForm.RemoveAll()
		assert.Equal(t, "gpt-transcribe", r.FormValue("model"))
		audio, header, err := r.FormFile("file")
		if !assert.NoError(t, err) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer audio.Close()
		assert.Equal(t, "audio.wav", header.Filename)
		size, err := io.Copy(io.Discard, audio)
		assert.NoError(t, err)
		assert.Greater(t, size, int64(44))
		uploaded.Store(size)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "fixture-audio-receipt")
		_, err = w.Write(
			[]byte(`{"text":"actual extracted audio transcript","usage":{"type":"duration","seconds":0.5}}`),
		)
		assert.NoError(t, err)
	}))
	defer fake.Close()
	transcriber, err := transcription.NewOpenAI(
		transcription.Options{
			Endpoint:   fake.URL + "/v1/audio/transcriptions",
			APIKey:     "fixture-only-key",
			HTTPClient: fake.Client(),
		},
	)
	require.NoError(t, err)
	tools.transcriber = transcriber
	tools.cfg.Transcription = config.Transcription{
		Enabled:            true,
		Provider:           "openai",
		Model:              "gpt-transcribe",
		CreditsPerSecond:   75,
		MaxDurationSeconds: 300,
	}
	blob, err := tools.files.Upload(
		ctx,
		artifacts.Principal{OwnerID: uid, OrgID: p.OrgID},
		"audio/wav",
		bytes.NewReader(oneSecondWAV(t)),
	)
	require.NoError(t, err)
	handle, err := tools.repository.CreateArtifactHandle(ctx, p, run.ID, blob.ID)
	require.NoError(t, err)
	args, err := json.Marshal(map[string]any{"handle_id": handle.ID, "language": "en"})
	require.NoError(t, err)
	intent, reserve, err := tools.Prepare(
		ctx,
		run,
		p,
		"",
		provider.Call{ID: "audio-acceptance", Name: "audio_transcribe", Arguments: args},
	)
	require.NoError(t, err)
	require.Positive(t, reserve)
	require.EqualValues(t, 75, intent.Billing.CreditsPerSecond)
	payload, err := json.Marshal(intent)
	require.NoError(t, err)
	step, err := tools.repository.BeginStep(ctx, run.ID, run.LeaseOwner, run.Fence, "transcription", payload, reserve)
	require.NoError(t, err)
	tools.cfg.Transcription.CreditsPerSecond = 999
	output, usage, err := tools.Execute(ctx, run, p, "", intent, step.ID)
	require.NoError(t, err)
	require.Positive(t, uploaded.Load())
	require.EqualValues(t, 500, usage.AudioDurationMS)
	require.Equal(t, "fixture-audio-receipt", usage.RequestID)
	cost, err := domain.PaidToolCost(usage, intent.Billing)
	require.NoError(t, err)
	require.EqualValues(t, 38, cost)
	usage.Microcredits = cost
	require.NoError(
		t,
		tools.repository.CompleteStep(
			ctx,
			run.ID,
			run.LeaseOwner,
			run.Fence,
			step.ID,
			provider.CallOutput(intent.Call.ID, output),
			usage,
		),
	)
	got, err := tools.repository.ExecutionRun(ctx, run.ID)
	require.NoError(t, err)
	require.EqualValues(t, 38, got.SpentMicrocredits)
	require.Zero(t, got.ReservedMicrocredits)
	var receipt struct {
		Text         string `json:"text"`
		TranscriptID string `json:"transcript_id"`
	}
	require.NoError(t, json.Unmarshal(output, &receipt))
	require.Equal(t, "actual extracted audio transcript", receipt.Text)
	require.NotEmpty(t, receipt.TranscriptID)
	page, err := tools.readTranscript(ctx, run, json.RawMessage(`{"transcript_id":"`+receipt.TranscriptID+`"}`))
	require.NoError(t, err)
	require.Contains(t, string(page), receipt.Text)
	require.NoError(t, tools.replayPaidTool(ctx, run, intent))
	revoked := p
	revoked.Permissions = maps.Clone(p.Permissions)
	delete(revoked.Permissions, "artifacts.read")
	tools.authorize = func(context.Context, domain.Run) (domain.Principal, string, error) { return revoked, "", nil }
	require.Error(t, tools.replayPaidTool(ctx, run, intent))
	tools.authorize = func(context.Context, domain.Run) (domain.Principal, string, error) { return p, "", nil }
	require.NoError(t, tools.replayPaidTool(ctx, run, intent))
	require.EqualValues(t, 1, requests.Load())
	exerciseDeletedAudioSource(t, tools, run, p, uid, blob.ID, &requests)
}

func oneSecondWAV(t *testing.T) []byte {
	t.Helper()
	const samples = 16000
	var wav bytes.Buffer
	wav.WriteString("RIFF")
	require.NoError(t, binary.Write(&wav, binary.LittleEndian, uint32(36+samples*2)))
	wav.WriteString("WAVEfmt ")
	for _, field := range []any{uint32(16), uint16(1), uint16(1), uint32(samples), uint32(samples * 2), uint16(2), uint16(16)} {
		require.NoError(t, binary.Write(&wav, binary.LittleEndian, field))
	}
	wav.WriteString("data")
	require.NoError(t, binary.Write(&wav, binary.LittleEndian, uint32(samples*2)))
	wav.Write(make([]byte, samples*2))
	return wav.Bytes()
}

func exerciseDeletedAudioSource(
	t *testing.T,
	tools runtimeTools,
	run domain.Run,
	p domain.Principal,
	uid, seedID string,
	requests *atomic.Int64,
) {
	t.Helper()
	ctx := t.Context()
	p.Permissions = maps.Clone(p.Permissions)
	p.Permissions["chat.delete"] = true
	originalChat, err := tools.repository.CreateChat(ctx, p)
	require.NoError(t, err)
	original, err := tools.files.DeriveToChat(
		ctx,
		p,
		uid,
		originalChat.ID,
		seedID,
		"audio/wav",
		bytes.NewReader(oneSecondWAV(t)),
	)
	require.NoError(t, err)
	handle, err := tools.repository.CreateArtifactHandle(ctx, p, run.ID, original.ID)
	require.NoError(t, err)
	args, err := json.Marshal(map[string]any{"handle_id": handle.ID})
	require.NoError(t, err)
	tools.cfg.Transcription.CreditsPerSecond = 75
	intent, reserve, err := tools.Prepare(
		ctx,
		run,
		p,
		"",
		provider.Call{ID: "deleted-audio", Name: "audio_transcribe", Arguments: args},
	)
	require.NoError(t, err)
	payload, err := json.Marshal(intent)
	require.NoError(t, err)
	step, err := tools.repository.BeginStep(ctx, run.ID, run.LeaseOwner, run.Fence, "transcription", payload, reserve)
	require.NoError(t, err)
	var prepared preparedAudio
	require.NoError(t, json.Unmarshal(intent.Prepared, &prepared))
	derived, err := tools.repository.LoadArtifactHandle(ctx, p, prepared.HandleID)
	require.NoError(t, err)
	require.NoError(t, tools.repository.DeleteChat(ctx, p, originalChat.ID))
	// The extracted WAV belongs to the active chat and remains readable independently.
	_, err = tools.files.Metadata(ctx, artifacts.Principal{OwnerID: uid, OrgID: p.OrgID}, derived.ArtifactID)
	require.NoError(t, err)
	output, usage, err := tools.Execute(ctx, run, p, "", intent, "must-not-dispatch")
	require.Error(t, err)
	var denied *runs.PaidToolError
	require.ErrorAs(t, err, &denied)
	require.Equal(t, "not_dispatched", denied.Disposition)
	require.Empty(t, output)
	require.Zero(t, usage.AudioDurationMS)
	require.EqualValues(t, 1, requests.Load())
	require.NoError(
		t,
		tools.repository.CompleteStep(
			ctx,
			run.ID,
			run.LeaseOwner,
			run.Fence,
			step.ID,
			provider.CallOutput(intent.Call.ID, json.RawMessage(`{"error":"transcription_rejected"}`)),
			domain.Usage{},
		),
	)
	got, err := tools.repository.ExecutionRun(ctx, run.ID)
	require.NoError(t, err)
	require.EqualValues(t, 38, got.SpentMicrocredits)
	require.Zero(t, got.ReservedMicrocredits)
}
