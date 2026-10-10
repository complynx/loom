package artifacts_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/complynx/clx-scripter/internal/artifacts"
	"github.com/complynx/clx-scripter/internal/domain"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

type metadataStore struct {
	mu        sync.Mutex
	rows      map[string]domain.Artifact
	ambiguous bool
}

func (m *metadataStore) CommitArtifact(_ context.Context, a domain.Artifact, quota int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var used int64
	for _, row := range m.rows {
		if row.OwnerID == a.OwnerID {
			used += row.Size
		}
	}
	if used+a.Size > quota {
		return errors.New("quota exceeded")
	}
	m.rows[a.ID] = a
	if m.ambiguous {
		return errors.New("commit response lost")
	}
	return nil
}
func (m *metadataStore) GetArtifact(_ context.Context, id string) (domain.Artifact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.rows[id]
	if !ok {
		return domain.Artifact{}, domain.ErrNotFound
	}
	return a, nil
}
func service(t *testing.T, max, quota int64) (*artifacts.Service, *metadataStore) {
	t.Helper()
	store := &metadataStore{rows: make(map[string]domain.Artifact)}
	s, err := artifacts.New(t.TempDir(), store, artifacts.Limits{MaxUploadBytes: max, QuotaBytes: quota})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s, store
}

var owner = artifacts.Principal{OwnerID: "user", OrgID: "org"}

func TestUploadAndReadImmutableArtifact(t *testing.T) {
	s, _ := service(t, 1024, 2048)
	content := []byte("hello artifact")
	a, err := s.Upload(context.Background(), owner, "", bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(content)
	if a.Size != int64(len(content)) || a.SHA256 != hex.EncodeToString(hash[:]) || a.MIME != "text/plain; charset=utf-8" {
		t.Fatalf("unexpected metadata: %+v", a)
	}
	metadata, r, err := s.Download(context.Background(), owner, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	}()
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, content) || metadata.ID != a.ID {
		t.Fatalf("download=%q err=%v", got, err)
	}
}
func TestUploadLimitsAndQuota(t *testing.T) {
	s, _ := service(t, 8, 10)
	if _, err := s.Upload(context.Background(), owner, "", bytes.NewBufferString("123456789")); !errors.Is(err, artifacts.ErrTooLarge) {
		t.Fatalf("oversize: %v", err)
	}
	if _, err := s.Upload(context.Background(), owner, "text/plain", bytes.NewBufferString("12345678")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Upload(context.Background(), owner, "text/plain", bytes.NewBufferString("123")); err == nil {
		t.Fatal("quota upload succeeded")
	}
}
func TestReadRejectsOtherOwnerAndOrganization(t *testing.T) {
	s, _ := service(t, 10, 20)
	a, err := s.Upload(context.Background(), owner, "text/plain", bytes.NewBufferString("ok"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []artifacts.Principal{{OwnerID: "other", OrgID: "org"}, {OwnerID: "user", OrgID: "other"}, {}} {
		if _, _, err := s.Download(context.Background(), p, a.ID); !errors.Is(err, artifacts.ErrForbidden) {
			t.Fatalf("unauthorized read: %v", err)
		}
	}
	for _, id := range []string{"../secret", "/etc/passwd", "..\\secret", a.ID + "/../secret"} {
		if _, _, err := s.Download(context.Background(), owner, id); !errors.Is(err, artifacts.ErrNotFound) {
			t.Fatalf("path accepted %q: %v", id, err)
		}
	}
}
func TestCanceledUploadAndRead(t *testing.T) {
	s, _ := service(t, 10, 20)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Upload(ctx, owner, "", bytes.NewBufferString("ok")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled upload: %v", err)
	}
	a, err := s.Upload(context.Background(), owner, "", bytes.NewBufferString("ok"))
	if err != nil {
		t.Fatal(err)
	}
	_, r, err := s.Download(ctx, owner, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := io.ReadAll(r); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read: %v", err)
	}
}
func TestLostCommitResponsePreservesUploadedContent(t *testing.T) {
	s, store := service(t, 10, 20)
	store.ambiguous = true
	a, err := s.Upload(context.Background(), owner, "", bytes.NewBufferString("ok"))
	if err != nil {
		t.Fatal(err)
	}
	_, r, err := s.Download(context.Background(), owner, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	}()
	got, err := io.ReadAll(r)
	if err != nil || string(got) != "ok" {
		t.Fatalf("lost committed content: %q %v", got, err)
	}
}
func TestExternalDisclosureRequiresOwnerAndExplicitDestination(t *testing.T) {
	a := domain.Artifact{OwnerID: owner.OwnerID, OrgID: owner.OrgID, ExternalDisclosureAllowed: true}
	if err := artifacts.AllowDisclosure(a, owner, "openai", []string{"openai"}); err != nil {
		t.Fatal(err)
	}
	if err := artifacts.AllowDisclosure(a, owner, "other", []string{"openai"}); !errors.Is(err, artifacts.ErrForbidden) {
		t.Fatal(err)
	}
	if err := artifacts.AllowDisclosure(a, artifacts.Principal{OwnerID: "other", OrgID: "org"}, "openai", []string{"openai"}); !errors.Is(err, artifacts.ErrForbidden) {
		t.Fatal(err)
	}
}

func TestReadRejectsReplacedSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows requires symlink privileges; production platform is Linux")
	}
	volume := t.TempDir()
	store := &metadataStore{rows: make(map[string]domain.Artifact)}
	s, err := artifacts.New(volume, store, artifacts.Limits{MaxUploadBytes: 100, QuotaBytes: 200})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	a, err := s.Upload(context.Background(), owner, "", bytes.NewBufferString("ok"))
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(volume, a.ID)
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../secret", target); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Download(context.Background(), owner, a.ID); !errors.Is(err, artifacts.ErrInvalid) {
		t.Fatalf("symlink accepted: %v", err)
	}
}
func TestConcurrentUploadsCannotExceedQuota(t *testing.T) {
	s, _ := service(t, 10, 10)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			_, err := s.Upload(context.Background(), owner, "", bytes.NewBufferString("12345678"))
			results <- err
		})
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("concurrent successful uploads=%d", succeeded)
	}
}

func TestModelImageRequiresDisclosureAndCapability(t *testing.T) {
	s, _ := service(t, 1024, 4096)
	var content bytes.Buffer
	if err := png.Encode(&content, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	a, err := s.Upload(context.Background(), owner, "image/png", bytes.NewReader(content.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	image, err := s.ForModel(context.Background(), owner, a.ID, "openai", []string{"openai"}, true, 1024)
	if err != nil || !bytes.Equal(image.Bytes, content.Bytes()) {
		t.Fatalf("forward image: %v", err)
	}
	if _, err := s.ForModel(context.Background(), owner, a.ID, "openai", nil, true, 1024); !errors.Is(err, artifacts.ErrForbidden) {
		t.Fatalf("disclosure not denied: %v", err)
	}
	if _, err := s.ForModel(context.Background(), owner, a.ID, "openai", []string{"openai"}, false, 1024); !errors.Is(err, artifacts.ErrForbidden) {
		t.Fatalf("capability not denied: %v", err)
	}
	if _, err := s.ForModel(context.Background(), owner, a.ID, "openai", []string{"openai"}, true, 1); !errors.Is(err, artifacts.ErrTooLarge) {
		t.Fatalf("size not denied: %v", err)
	}
	forged, err := s.Upload(context.Background(), owner, "image/png", bytes.NewBufferString("this is text"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ForModel(context.Background(), owner, forged.ID, "openai", []string{"openai"}, true, 1024); !errors.Is(err, artifacts.ErrInvalid) {
		t.Fatalf("forged MIME not denied: %v", err)
	}
}

// blockedUploadInput represents clients that opened an upload but have not sent
// content. Each entry signal is an observable read of the supplied request body.
type blockedUploadInput struct {
	entered chan<- struct{}
	release <-chan struct{}
	once    sync.Once
}

func (r *blockedUploadInput) Read([]byte) (int, error) {
	r.once.Do(func() { r.entered <- struct{}{} })
	<-r.release
	return 0, io.EOF
}
func TestConcurrentUploadLimitCancelsQueuedClientBeforeReading(t *testing.T) {
	s, store := service(t, 10, 100)
	entered := make(chan struct{}, 5)
	release := make(chan struct{})
	activeCtx, cancelActive := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			_, err := s.Upload(activeCtx, owner, "", &blockedUploadInput{entered: entered, release: release})
			if !errors.Is(err, context.Canceled) {
				t.Errorf("active canceled upload: %v", err)
			}
		})
	}
	defer wg.Wait()
	defer close(release)
	defer cancelActive()
	for range 4 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("default four upload slots did not start")
		}
	}
	queuedCtx, cancelQueued := context.WithCancel(context.Background())
	defer cancelQueued()
	result := make(chan error, 1)
	go func() {
		_, err := s.Upload(queuedCtx, owner, "", &blockedUploadInput{entered: entered, release: release})
		result <- err
	}()
	cancelQueued()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("queued cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued upload cancellation blocked")
	}
	select {
	case <-entered:
		t.Fatal("queued canceled upload read its request body")
	default:
	}
	store.mu.Lock()
	committed := len(store.rows)
	store.mu.Unlock()
	if committed != 0 {
		t.Fatal("blocked or canceled upload committed metadata")
	}
}
