package app

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/fileproc"
	"github.com/complynx/loom/internal/provider"
	"github.com/complynx/loom/internal/runs"
	"github.com/complynx/loom/internal/store"
	"github.com/complynx/loom/internal/transcription"
)

func transcriptFixture(t *testing.T) (*fileReferenceFixture, *atomic.Int32, *atomic.Int32, string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("file worker uses Unix sockets")
	}
	f := referenceFixture(t)
	workers, calls := new(atomic.Int32), new(atomic.Int32)
	text := strings.Repeat("long transcript 猫\n", 4000)
	socket := filepath.Join(t.TempDir(), "w.sock")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	handler := &fileproc.Handler{
		LocalProcessor: func(_ context.Context, _ fileproc.Request, _, output string) (fileproc.Result, error) {
			workers.Add(1)
			path := filepath.Join(output, "audio.wav")
			if writeErr := os.WriteFile(path, oneSecondWAV(t), 0600); writeErr != nil {
				return fileproc.Result{}, writeErr
			}
			return fileproc.Result{
				Data:    json.RawMessage(`{"has_audio":true,"start":0,"end":1}`),
				Outputs: []fileproc.Output{{Name: "audio.wav", MIME: "audio/wav", Path: path}},
			}, nil
		},
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	asr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		_ = json.NewEncoder(w).
			Encode(map[string]any{"text": text, "usage": map[string]any{"type": "duration", "seconds": 1}})
	}))
	t.Cleanup(asr.Close)
	f.tools.transcriber, err = transcription.NewOpenAI(
		transcription.Options{
			Endpoint:   asr.URL + "/audio/transcriptions",
			APIKey:     "fixture-only",
			HTTPClient: asr.Client(),
		},
	)
	require.NoError(t, err)
	f.tools.cfg.FileTools = config.FileTools{Enabled: true, Socket: socket, TimeoutMS: 5000}
	f.tools.cfg.Transcription = config.Transcription{
		Enabled:            true,
		Provider:           "openai",
		Model:              "fixture-asr",
		CreditsPerSecond:   75,
		MaxDurationSeconds: 300,
	}
	return f, workers, calls, text
}

func settleTranscript(t *testing.T, f *fileReferenceFixture, call provider.Call) (domain.PaidToolIntent, string) {
	t.Helper()
	ctx := t.Context()
	intent, reserve, err := f.tools.Prepare(ctx, f.run, f.p, "", call)
	require.NoError(t, err)
	raw, err := json.Marshal(intent)
	require.NoError(t, err)
	step, err := f.tools.repository.BeginStep(
		ctx,
		f.run.ID,
		f.run.LeaseOwner,
		f.run.Fence,
		"transcription",
		raw,
		reserve,
	)
	require.NoError(t, err)
	output, usage, err := f.tools.Execute(ctx, f.run, f.p, "", intent, step.ID)
	require.NoError(t, err)
	usage.Microcredits, err = domain.PaidToolCost(usage, intent.Billing)
	require.NoError(t, err)
	require.NoError(
		t,
		f.tools.repository.CompleteStep(
			ctx,
			f.run.ID,
			f.run.LeaseOwner,
			f.run.Fence,
			step.ID,
			provider.CallOutput(call.ID, output),
			usage,
		),
	)
	var receipt struct {
		ID      string `json:"transcript_id"`
		Partial bool   `json:"partial"`
	}
	require.NoError(t, json.Unmarshal(output, &receipt))
	require.True(t, receipt.Partial)
	return intent, receipt.ID
}

func TestASRDurableReuse(t *testing.T) {
	t.Parallel()
	f, workers, calls, text := transcriptFixture(t)
	ctx := t.Context()
	call := provider.Call{
		ID:        "first",
		Name:      "audio_transcribe",
		Arguments: json.RawMessage(`{"artifact_id":"` + f.first.ID + `","language":"en"}`),
	}
	intent, tid := settleTranscript(t, f, call)
	require.EqualValues(t, 1, workers.Load())
	require.EqualValues(t, 1, calls.Load())
	call.ID = "distinct"
	call.Arguments = json.RawMessage(`{"artifact_id":"` + f.first.ID + `","language":"en","reuse_only":true}`)
	output, found, err := f.tools.Reuse(ctx, f.run, f.p, "", call)
	require.NoError(t, err)
	require.True(t, found)
	require.Contains(t, string(output), `"reused":true`)
	// Expire/collect both original and extracted binary, and make upstream fetch fail.
	_, err = f.tools.repository.Pool().Exec(ctx, "UPDATE artifacts SET expires_at=now()-interval '1 second'")
	require.NoError(t, err)
	_, err = f.tools.repository.SweepLifecycle(ctx, 100)
	require.NoError(t, err)
	_, err = f.tools.files.CollectDeleted(ctx, 100)
	require.NoError(t, err)
	f.changed.Store(true)
	fetches, downloads := f.fetches.Load(), f.downloads.Load()
	require.NoError(t, f.tools.replayPaidTool(ctx, f.run, intent))
	require.NoError(
		t,
		f.tools.repository.Transition(ctx, f.run.ID, f.run.LeaseOwner, f.run.Fence, domain.Completed, "", "done"),
	)
	chat, err := f.tools.repository.CreateChat(ctx, f.p)
	require.NoError(t, err)
	next, err := f.tools.repository.CreateRun(
		ctx,
		f.p,
		"next",
		domain.CreateRun{
			ChatID: chat.ID,
			Input: domain.Input{
				Text: "Read the complete stored transcript for source artifact " + f.first.ID + " with language en, no prompt and default start/end. Do not reprocess it.",
			},
		},
	)
	require.NoError(t, err)
	f.run, err = f.tools.repository.ClaimRun(ctx, next.ID, "worker", time.Minute)
	require.NoError(t, err)
	// Rebuild runtime value and change model/provider configuration. Neither changes an existing identity.
	f.tools = runtimeTools{
		repository: store.New(f.tools.repository.Pool(), nil),
		files:      f.tools.files,
		registry:   f.tools.registry,
		authorize:  f.tools.authorize,
		bridge:     f.tools.bridge,
		cfg:        f.tools.cfg,
	}
	f.tools.cfg.Transcription.Model = "new-model"
	output, found, err = f.tools.Reuse(ctx, f.run, f.p, "", call)
	require.NoError(t, err)
	require.True(t, found)
	require.Contains(t, string(output), `"model":"fixture-asr"`)
	var complete strings.Builder
	offset := 0
	for {
		raw, _ := json.Marshal(map[string]any{"transcript_id": tid, "offset": offset, "limit": 8192})
		page, readErr := f.tools.readTranscript(ctx, f.run, raw)
		require.NoError(t, readErr)
		var out struct {
			Text    string `json:"text"`
			Next    int    `json:"next_offset"`
			Partial bool   `json:"partial"`
		}
		require.NoError(t, json.Unmarshal(page, &out))
		complete.WriteString(out.Text)
		offset = out.Next
		if !out.Partial {
			break
		}
	}
	require.Equal(t, text, complete.String())
	model := &transcriptModel{call: call}
	_, err = f.tools.repository.Pool().
		Exec(ctx, "UPDATE runs SET lease_until=now()-interval '1 second' WHERE id=$1", f.run.ID)
	require.NoError(t, err)
	engine := runs.New(
		f.tools.repository,
		model,
		runs.Options{
			Owner:          "worker",
			Authorize:      f.tools.authorize,
			Tools:          f.tools.tools,
			PaidTool:       f.tools,
			PaidToolReplay: f.tools.replayPaidTool,
		},
	)
	require.NoError(t, engine.Execute(ctx, f.run.ID))
	require.Len(t, model.requests, 2)
	request, _ := json.Marshal(model.requests[1])
	require.Contains(t, string(request), `\"reused\":true`)
	if outputDir := os.Getenv("LOOM_ASR_CAPTURE_DIR"); outputDir != "" {
		for i, request := range model.requests {
			raw, _ := json.Marshal(request)
			require.NoError(
				t,
				os.WriteFile(
					filepath.Join(outputDir, []string{"initial-request.json", "reused-request.json"}[i]),
					raw,
					0600,
				),
			)
		}
	}
	var cacheLedger int
	require.NoError(
		t,
		f.tools.repository.Pool().
			QueryRow(ctx, `SELECT count(*) FROM credit_ledger l JOIN steps s ON s.id=l.step_id WHERE s.kind='transcript_reuse'`).
			Scan(&cacheLedger),
	)
	require.Zero(t, cacheLedger)
	require.Equal(t, fetches, f.fetches.Load())
	require.Equal(t, downloads, f.downloads.Load())
	require.EqualValues(t, 1, workers.Load())
	require.EqualValues(t, 1, calls.Load())
	var reserves, settlements int
	require.NoError(
		t,
		f.tools.repository.Pool().
			QueryRow(ctx, `SELECT count(*) FILTER (WHERE l.kind='reserve'),count(*) FILTER (WHERE l.kind='settle') FROM credit_ledger l JOIN steps s ON s.id=l.step_id WHERE s.kind='transcription'`).
			Scan(&reserves, &settlements),
	)
	require.Equal(t, 1, reserves)
	require.Equal(t, 1, settlements)
	delete(f.p.Permissions, "qa.read")
	_, _, err = f.tools.Reuse(ctx, f.run, f.p, "", call)
	require.Error(t, err)
	_, err = f.tools.readTranscript(ctx, f.run, json.RawMessage(`{"transcript_id":"`+tid+`"}`))
	require.Error(t, err)
	f.p.Permissions["qa.read"] = true
	foreign := f.p
	foreign.Subject, foreign.Actor = "bob", "bob"
	_, err = f.tools.repository.ResolveUser(ctx, foreign)
	require.NoError(t, err)
	_, err = f.tools.repository.ReadTranscript(ctx, foreign, tid)
	require.Error(t, err)
}

func TestASRReuseOnlyMissAndDefiniteRejection(t *testing.T) {
	t.Parallel()
	f, workers, calls, _ := transcriptFixture(t)
	ctx := t.Context()
	call := provider.Call{
		ID:        "lookup",
		Name:      "audio_transcribe",
		Arguments: json.RawMessage(`{"artifact_id":"` + f.first.ID + `","reuse_only":true}`),
	}
	output, handled, err := f.tools.Reuse(ctx, f.run, f.p, "", call)
	require.NoError(t, err)
	require.True(t, handled)
	require.JSONEq(t, `{"error":"transcript_not_found"}`, string(output))
	_, _, err = f.tools.Prepare(ctx, f.run, f.p, "", call)
	require.Error(t, err)
	require.Zero(t, workers.Load())
	require.Zero(t, calls.Load())
	call.Arguments = json.RawMessage(`{"artifact_id":"` + f.first.ID + `"}`)
	intent, reserve, err := f.tools.Prepare(ctx, f.run, f.p, "", call)
	require.NoError(t, err)
	input, _ := json.Marshal(intent)
	step, err := f.tools.repository.BeginStep(
		ctx,
		f.run.ID,
		f.run.LeaseOwner,
		f.run.Fence,
		"transcription",
		input,
		reserve,
	)
	require.NoError(t, err)
	require.NoError(t, f.tools.repository.ClaimTranscriptDispatch(ctx, f.run, step.ID))
	// A definite provider rejection is settled at zero, retaining the audit step.
	require.NoError(
		t,
		f.tools.repository.CompleteStep(
			ctx,
			f.run.ID,
			f.run.LeaseOwner,
			f.run.Fence,
			step.ID,
			provider.CallOutput(call.ID, json.RawMessage(`{"error":"transcription_rejected"}`)),
			domain.Usage{},
		),
	)
	_, handled, err = f.tools.Reuse(ctx, f.run, f.p, "", call)
	require.NoError(t, err)
	require.False(t, handled)
	_, err = f.tools.repository.BeginStep(ctx, f.run.ID, f.run.LeaseOwner, f.run.Fence, "transcription", input, reserve)
	require.NoError(t, err)
	require.Zero(t, calls.Load())
}

func TestASRConcurrentAdmissionAndUnknownFence(t *testing.T) {
	t.Parallel()
	f, _, calls, _ := transcriptFixture(t)
	ctx := t.Context()
	call := provider.Call{
		ID:        "first",
		Name:      "audio_transcribe",
		Arguments: json.RawMessage(`{"artifact_id":"` + f.first.ID + `"}`),
	}
	intent, reserve, err := f.tools.Prepare(ctx, f.run, f.p, "", call)
	require.NoError(t, err)
	var successes atomic.Int32
	var wg sync.WaitGroup
	chat, err := f.tools.repository.CreateChat(ctx, f.p)
	require.NoError(t, err)
	other, err := f.tools.repository.CreateRun(
		ctx,
		f.p,
		"concurrent",
		domain.CreateRun{ChatID: chat.ID, Input: domain.Input{Text: "Concurrent ASR"}},
	)
	require.NoError(t, err)
	other, err = f.tools.repository.ClaimRun(ctx, other.ID, "worker", time.Minute)
	require.NoError(t, err)
	otherCall := call
	otherCall.ID = "second"
	otherIntent, _, err := f.tools.Prepare(ctx, other, f.p, "", otherCall)
	require.NoError(t, err)
	type admitted struct {
		run    domain.Run
		intent domain.PaidToolIntent
	}
	winner := make(chan admitted, 1)
	initialRun := f.run
	for _, attempt := range []admitted{{f.run, intent}, {other, otherIntent}} {
		wg.Go(func() {
			raw, _ := json.Marshal(attempt.intent)
			_, admitErr := f.tools.repository.BeginStep(
				ctx,
				attempt.run.ID,
				attempt.run.LeaseOwner,
				attempt.run.Fence,
				"transcription",
				raw,
				reserve,
			)
			if admitErr == nil {
				successes.Add(1)
				winner <- attempt
			}
		})
	}
	wg.Wait()
	require.EqualValues(t, 1, successes.Load())
	selected := <-winner
	f.run = selected.run
	intent = selected.intent
	var sid string
	require.NoError(
		t,
		f.tools.repository.Pool().QueryRow(ctx, "SELECT id::text FROM steps WHERE kind='transcription'").Scan(&sid),
	)
	_, _, err = f.tools.Execute(ctx, f.run, f.p, "", intent, sid)
	require.NoError(t, err)
	require.NoError(t, f.tools.repository.MarkStepUnknown(ctx, f.run.ID, f.run.LeaseOwner, f.run.Fence, sid))
	_, found, err := f.tools.Reuse(ctx, f.run, f.p, "", call)
	require.Error(t, err)
	require.False(t, found)
	require.Error(t, f.tools.repository.ClaimTranscriptDispatch(ctx, f.run, sid))
	require.EqualValues(t, 1, calls.Load())
	// Explicitly different processing parameters have different identities; model changes do not.
	_, one, err := f.tools.audioIdentity(ctx, f.run, f.p, audioArguments{ArtifactID: f.first.ID})
	require.NoError(t, err)
	_, two, err := f.tools.audioIdentity(ctx, f.run, f.p, audioArguments{ArtifactID: f.first.ID, Language: "ru"})
	require.NoError(t, err)
	require.NotEqual(t, one, two)
	call.Arguments = json.RawMessage(`{"artifact_id":"` + f.first.ID + `","reprocess_key":"` + uuid.NewString() + `"}`)
	_, _, err = f.tools.Reuse(ctx, f.run, f.p, "", call)
	require.NoError(t, err) // Admission, not lookup, fences another explicit attempt.
	loser := other
	if selected.run.ID == other.ID {
		loser = initialRun
	}
	intent.ReprocessKey = uuid.NewString()
	raw, _ := json.Marshal(intent)
	_, err = f.tools.repository.BeginStep(ctx, loser.ID, loser.LeaseOwner, loser.Fence, "transcription", raw, reserve)
	require.Error(t, err)
	var paidSteps int
	require.NoError(
		t,
		f.tools.repository.Pool().
			QueryRow(ctx, "SELECT count(*) FROM steps WHERE kind='transcription'").
			Scan(&paidSteps),
	)
	require.Equal(t, 1, paidSteps)
}

type transcriptModel struct {
	call     provider.Call
	requests []provider.Request
}

func (m *transcriptModel) Respond(_ context.Context, request provider.Request) (provider.Response, error) {
	m.requests = append(m.requests, request)
	if len(m.requests) == 1 {
		output, _ := json.Marshal(
			map[string]any{
				"type":      "function_call",
				"name":      m.call.Name,
				"call_id":   m.call.ID,
				"arguments": string(m.call.Arguments),
			},
		)
		return provider.Response{
			ID:     uuid.NewString(),
			Calls:  []provider.Call{m.call},
			Output: []provider.Item{output},
		}, nil
	}
	return provider.Response{
		ID:     uuid.NewString(),
		Text:   "Transcript available",
		Output: []provider.Item{provider.Message("assistant", "Transcript available")},
	}, nil
}
