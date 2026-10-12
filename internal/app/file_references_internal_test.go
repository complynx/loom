package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/acceptance"
	"github.com/complynx/loom/internal/artifacts"
	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/modules"
)

type fileReferenceFixture struct {
	tools             runtimeTools
	run               domain.Run
	p                 domain.Principal
	first             domain.Artifact
	root              string
	fetches           atomic.Int32
	downloads         atomic.Int32
	changed           atomic.Bool
	slow              atomic.Bool
	disclosureRevoked atomic.Bool
}

func referenceFixture(t *testing.T) *fileReferenceFixture {
	t.Helper()
	repository, _ := searchToolStore(t)
	f := &fileReferenceFixture{root: t.TempDir()}
	f.p = domain.Principal{
		Issuer:    "issuer",
		Subject:   "alice",
		Actor:     "alice",
		OrgID:     "org",
		ExpiresAt: time.Now().Add(time.Hour),
		Permissions: map[string]bool{
			"runs.create":    true,
			"runs.control":   true,
			"chat.read":      true,
			"artifacts.read": true,
			"qa.access":      true,
			"qa.read":        true,
		},
	}
	run, err := repository.CreateRun(
		t.Context(),
		f.p,
		"run",
		domain.CreateRun{Input: domain.Input{Text: "Keep same run"}},
	)
	require.NoError(t, err)
	f.run, err = repository.ClaimRun(t.Context(), run.ID, "worker", time.Minute)
	require.NoError(t, err)
	files, err := artifacts.New(f.root, repository, artifacts.Limits{MaxUploadBytes: 2 << 20, QuotaBytes: 8 << 20})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, files.Close()) })
	fixture, err := acceptance.New(filepath.Join(t.TempDir(), "state.json"), "read-token", "write-token")
	require.NoError(t, err)
	image, err := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jfWQAAAAASUVORK5CYII=",
	)
	require.NoError(t, err)
	server := httptest.NewServer(f.referenceHandler(t, fixture.Handler(), image))
	t.Cleanup(server.Close)
	address, err := url.Parse(server.URL)
	require.NoError(t, err)
	registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		return []modules.Endpoint{
			{ContainerID: "fixture", URI: "http://registered:" + address.Port() + "/manifest", Address: "127.0.0.1"},
		}, nil
	}), modules.Options{ImportArtifact: importModuleArtifact(repository, &files)})
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(t.Context()))
	authorize := func(context.Context, domain.Run) (domain.Principal, string, error) { return f.p, "read-token", nil }
	f.tools = runtimeTools{
		repository: repository,
		files:      files,
		registry:   registry,
		authorize:  authorize,
		cfg: config.Config{
			Providers: map[string]config.Provider{"openai": {}},
			Artifacts: config.Artifacts{MaxUploadBytes: 2 << 20},
		},
	}
	f.tools.bridge = NewModuleBridge(registry, nil, repository, authorize)
	raw, err := f.tools.fileBroker(f.run).
		Call(t.Context(), "fixture", "fetch_file", json.RawMessage(`{"external_id":"fixture-file"}`))
	require.NoError(t, err)
	var result struct {
		Artifacts []domain.Artifact `json:"artifacts"`
	}
	require.NoError(t, json.Unmarshal(raw, &result))
	require.Len(t, result.Artifacts, 1)
	f.first = result.Artifacts[0]
	require.NotEmpty(t, f.first.SourceReferenceID)
	return f
}

func (f *fileReferenceFixture) expire(t *testing.T) {
	t.Helper()
	ref, err := f.tools.repository.FileReference(t.Context(), f.p, f.first.ID)
	require.NoError(t, err)
	_, err = f.tools.repository.Pool().
		Exec(t.Context(), "UPDATE artifacts SET expires_at=now()-interval '1 second' WHERE id=$1", ref.CurrentID)
	require.NoError(t, err)
	_, err = f.tools.repository.SweepLifecycle(t.Context(), 100)
	require.NoError(t, err)
	_, err = f.tools.files.CollectDeleted(t.Context(), 100)
	require.NoError(t, err)
}

func TestExternalFileRefreshIsSharedAndPreservesSameRunModelAndPaidReplay(t *testing.T) {
	t.Parallel()
	f := referenceFixture(t)
	handle, err := f.tools.repository.CreateArtifactHandle(t.Context(), f.p, f.run.ID, f.first.ID)
	require.NoError(t, err)
	f.expire(t)
	var wg sync.WaitGroup
	got := make([]domain.Artifact, 4)
	errs := make([]error, 4)
	for i := range got {
		wg.Go(func() { got[i], errs[i] = f.tools.resolveArtifact(t.Context(), f.run, f.p, f.first.ID) })
	}
	wg.Wait()
	for i := range got {
		require.NoError(t, errs[i])
		require.NotEqual(t, f.first.ID, got[i].ID)
		require.Equal(t, got[0].ID, got[i].ID)
		require.Equal(t, f.first.SHA256, got[i].SHA256)
	}
	require.EqualValues(t, 2, f.fetches.Load())
	require.EqualValues(t, 2, f.downloads.Load())
	_, err = os.Stat(filepath.Join(f.root, f.first.ID))
	require.ErrorIs(t, err, os.ErrNotExist)
	source, err := f.tools.resolveFileHandle(t.Context(), f.run, f.p, handle.ID)
	require.NoError(t, err)
	require.Equal(t, got[0].ID, source.ID)
	f.run.Input.ArtifactIDs = []string{f.first.ID}
	items, err := f.tools.modelArtifacts(t.Context(), f.run)
	require.NoError(t, err)
	require.Contains(t, string(items[0]), "input_image")
	err = f.tools.authorizeAudioSource(t.Context(), f.run, preparedAudio{SourceArtifactID: f.first.ID})
	require.NoError(t, err)
	require.EqualValues(t, 2, f.fetches.Load())
	active, err := f.tools.repository.GetRun(t.Context(), f.p, f.run.ID)
	require.NoError(t, err)
	require.Equal(t, domain.Running, active.State)
	require.Zero(t, active.SpentMicrocredits)
	require.Zero(t, active.ReservedMicrocredits)
	// Admission accepts the stable old ID in another chat; direct bytes stay expired.
	nextChat, err := f.tools.repository.CreateChat(t.Context(), f.p)
	require.NoError(t, err)
	next, err := f.tools.repository.CreateRun(
		t.Context(),
		f.p,
		"next",
		domain.CreateRun{
			ChatID: nextChat.ID,
			Input:  domain.Input{Text: "Reuse reference", ArtifactIDs: []string{f.first.ID}},
		},
	)
	require.NoError(t, err)
	require.NotEqual(t, f.run.ID, next.ID)
	_, err = f.tools.files.Metadata(
		t.Context(),
		artifacts.Principal{OwnerID: f.run.OwnerID, OrgID: f.p.OrgID},
		f.first.ID,
	)
	require.ErrorIs(t, err, artifacts.ErrNotFound)
}

func TestChangedFileRejectedBeforeBlobDownloadOrQuotaConsumption(t *testing.T) {
	t.Parallel()
	f := referenceFixture(t)
	f.expire(t)
	f.changed.Store(true)
	_, err := f.tools.resolveArtifact(t.Context(), f.run, f.p, f.first.ID)
	var failure *domain.Error
	require.ErrorAs(t, err, &failure)
	require.Equal(t, "file_version_changed", failure.Code)
	require.EqualValues(t, 1, f.downloads.Load())
	var count int
	require.NoError(t, f.tools.repository.Pool().QueryRow(t.Context(), "SELECT count(*) FROM artifacts").Scan(&count))
	require.Equal(t, 1, count)
	f.changed.Store(false)
	_, err = f.tools.resolveArtifact(t.Context(), f.run, f.p, f.first.ID)
	require.NoError(t, err)
}

func TestFileReferenceChecksCurrentServiceRightsAndCannotResurrectDeletedChat(t *testing.T) {
	t.Parallel()
	f := referenceFixture(t)
	delete(f.p.Permissions, "qa.read")
	_, err := f.tools.resolveArtifact(t.Context(), f.run, f.p, f.first.ID)
	require.Error(t, err)
	require.EqualValues(t, 1, f.fetches.Load())
	f.p.Permissions["qa.read"] = true
	f.p.Permissions["chat.delete"] = true
	require.NoError(t, f.tools.repository.DeleteChat(t.Context(), f.p, f.run.ChatID))
	_, err = f.tools.resolveArtifact(t.Context(), f.run, f.p, f.first.ID)
	require.Error(t, err)
	require.EqualValues(t, 1, f.fetches.Load())
	foreign := f.p
	foreign.Subject = "other"
	_, err = f.tools.repository.FileReference(t.Context(), foreign, f.first.ID)
	require.ErrorIs(t, err, domain.ErrNotFound)
}

func TestAbandonedRefreshLeaseRecoversWithoutTouchingUncertainCredits(t *testing.T) {
	t.Parallel()
	f := referenceFixture(t)
	f.expire(t)
	ref, err := f.tools.repository.FileReference(t.Context(), f.p, f.first.ID)
	require.NoError(t, err)
	_, claimed, err := f.tools.repository.ClaimFileRefresh(t.Context(), ref)
	require.NoError(t, err)
	require.True(t, claimed)
	_, err = f.tools.resolveArtifact(t.Context(), f.run, f.p, f.first.ID)
	require.NoError(t, err)
	step, err := f.tools.repository.BeginStep(
		t.Context(),
		f.run.ID,
		"worker",
		f.run.Fence,
		"transcription",
		json.RawMessage(`{}`),
		5,
	)
	require.NoError(t, err)
	require.NoError(t, f.tools.repository.MarkStepUnknown(t.Context(), f.run.ID, "worker", f.run.Fence, step.ID))
	_, err = f.tools.repository.Control(t.Context(), f.p, f.run.ID, "resume", "resume")
	require.Error(t, err)
	active, err := f.tools.repository.GetRun(t.Context(), f.p, f.run.ID)
	require.NoError(t, err)
	require.EqualValues(t, 5, active.ReservedMicrocredits)
	require.Zero(t, active.SpentMicrocredits)
	require.EqualValues(t, 2, f.fetches.Load())
}

func (f *fileReferenceFixture) referenceHandler(t *testing.T, handler http.Handler, image []byte) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/image" {
			f.downloads.Add(1)
			if r.Header.Get("Authorization") != "Bearer read-token" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(image)
			return
		}
		if r.URL.Path == "/fetch-file" && f.slow.Load() {
			select {
			case <-time.After(3 * time.Second):
			case <-r.Context().Done():
				return
			}
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		if r.URL.Path == "/manifest" || r.URL.Path == "/fetch-file" && rec.Code == http.StatusOK {
			body, err := f.referenceReply(r.URL.Path, rec.Body.Bytes(), image)
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			rec.Body.Reset()
			_, _ = rec.Body.Write(body)
		}
		maps.Copy(w.Header(), rec.Header())
		w.WriteHeader(rec.Code)
		_, _ = io.Copy(w, rec.Body)
	})
}

func sha256Bytes(data []byte) []byte { digest := sha256.Sum256(data); return digest[:] }

func (f *fileReferenceFixture) referenceReply(path string, raw, image []byte) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if path == "/manifest" {
		for _, m := range doc["modules"].([]any)[0].(map[string]any)["methods"].([]any) {
			if m.(map[string]any)["id"] == "fetch_file" {
				m.(map[string]any)["artifact_mime_types"] = []string{"text/plain", "image/png"}
				m.(map[string]any)["timeout_ms"] = 10000
				m.(map[string]any)["external_disclosure"] = !f.disclosureRevoked.Load()
			}
		}
	} else {
		f.fetches.Add(1)
		a := doc["artifacts"].([]any)[0].(map[string]any)
		a["download_path"] = "/image"
		a["mime_type"] = "image/png"
		a["size_bytes"] = len(image)
		a["sha256"] = hex.EncodeToString(sha256Bytes(image))
		if f.changed.Load() {
			a["sha256"] = strings.Repeat("a", 64)
		}
	}
	return json.Marshal(doc)
}

func TestFileReferenceChecksCurrentDisclosureForCachedBytes(t *testing.T) {
	t.Parallel()
	f := referenceFixture(t)
	f.disclosureRevoked.Store(true)
	require.NoError(t, f.tools.registry.Refresh(t.Context()))
	_, err := f.tools.resolveArtifact(t.Context(), f.run, f.p, f.first.ID)
	require.ErrorIs(t, err, artifacts.ErrForbidden)
	require.EqualValues(t, 1, f.fetches.Load())
	require.EqualValues(t, 1, f.downloads.Load())
}

func TestRefreshedFileSurvivesDeletionOfItsOriginalChat(t *testing.T) {
	t.Parallel()
	f := referenceFixture(t)
	f.expire(t)
	refreshed, err := f.tools.resolveArtifact(t.Context(), f.run, f.p, f.first.ID)
	require.NoError(t, err)
	chat, err := f.tools.repository.CreateChat(t.Context(), f.p)
	require.NoError(t, err)
	next, err := f.tools.repository.CreateRun(
		t.Context(),
		f.p,
		"other",
		domain.CreateRun{ChatID: chat.ID, Input: domain.Input{Text: "Keep file", ArtifactIDs: []string{refreshed.ID}}},
	)
	require.NoError(t, err)
	next, err = f.tools.repository.ClaimRun(t.Context(), next.ID, "other-worker", time.Minute)
	require.NoError(t, err)
	f.p.Permissions["chat.delete"] = true
	require.NoError(t, f.tools.repository.DeleteChat(t.Context(), f.p, f.run.ChatID))
	source, err := f.tools.resolveArtifact(t.Context(), next, f.p, refreshed.ID)
	require.NoError(t, err)
	require.Equal(t, refreshed.ID, source.ID)
	source, err = f.tools.resolveArtifact(t.Context(), next, f.p, f.first.ID)
	require.NoError(t, err)
	require.Equal(t, refreshed.ID, source.ID)
	require.EqualValues(t, 2, f.fetches.Load())
	require.NoError(t, f.tools.repository.DeleteChat(t.Context(), f.p, next.ChatID))
	_, err = f.tools.resolveArtifact(t.Context(), next, f.p, f.first.ID)
	require.Error(t, err)
}

func TestPausedRunKeepsExpiredHandleReferenceForImageReplay(t *testing.T) {
	t.Parallel()
	f := referenceFixture(t)
	handle, err := f.tools.repository.CreateArtifactHandle(t.Context(), f.p, f.run.ID, f.first.ID)
	require.NoError(t, err)
	require.NoError(
		t,
		f.tools.repository.Transition(t.Context(), f.run.ID, "worker", f.run.Fence, domain.Paused, "client_pause", ""),
	)
	_, err = f.tools.repository.Pool().
		Exec(t.Context(), "UPDATE artifact_handles SET expires_at=now()-interval '1 second' WHERE id=$1", handle.ID)
	require.NoError(t, err)
	f.expire(t)
	_, err = f.tools.repository.LoadArtifactHandle(t.Context(), f.p, handle.ID)
	require.Error(t, err)
	_, err = f.tools.repository.Control(t.Context(), f.p, f.run.ID, "resume", "resume")
	require.NoError(t, err)
	f.run, err = f.tools.repository.ClaimRun(t.Context(), f.run.ID, "worker", time.Minute)
	require.NoError(t, err)
	items, err := f.tools.attachArtifact(t.Context(), f.run, handle.ID)
	require.NoError(t, err)
	require.Contains(t, string(items[0]), "input_image")
	require.EqualValues(t, 2, f.fetches.Load())
}

func TestRefreshHeartbeatProtectsSlowFetchFromDuplicateDispatch(t *testing.T) {
	t.Parallel()
	f := referenceFixture(t)
	f.expire(t)
	f.slow.Store(true)
	var wg sync.WaitGroup
	got := make([]domain.Artifact, 2)
	errs := make([]error, 2)
	for i := range got {
		wg.Go(func() { got[i], errs[i] = f.tools.resolveArtifact(t.Context(), f.run, f.p, f.first.ID) })
	}
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	require.Equal(t, got[0].ID, got[1].ID)
	require.EqualValues(t, 2, f.fetches.Load())
	require.EqualValues(t, 2, f.downloads.Load())
}

func TestCancelledRefreshReleasesLeaseAndCanBeRetried(t *testing.T) {
	t.Parallel()
	f := referenceFixture(t)
	f.expire(t)
	f.slow.Store(true)
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	_, err := f.tools.resolveArtifact(ctx, f.run, f.p, f.first.ID)
	require.Error(t, err)
	f.slow.Store(false)
	_, err = f.tools.resolveArtifact(t.Context(), f.run, f.p, f.first.ID)
	require.NoError(t, err)
	require.EqualValues(t, 2, f.fetches.Load())
	require.EqualValues(t, 2, f.downloads.Load())
}
