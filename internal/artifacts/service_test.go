package artifacts_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/complynx/loom/internal/artifacts"
	"github.com/complynx/loom/internal/domain"
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
func service(t *testing.T, maxUpload, quota int64) (*artifacts.Service, *metadataStore) {
	t.Helper()
	store := &metadataStore{rows: make(map[string]domain.Artifact)}
	s, err := artifacts.New(t.TempDir(), store, artifacts.Limits{MaxUploadBytes: maxUpload, QuotaBytes: quota})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := s.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	return s, store
}

func testOwner() artifacts.Principal { return artifacts.Principal{OwnerID: "user", OrgID: "org"} }

func TestUploadAndReadImmutableArtifact(t *testing.T) {
	t.Parallel()
	s, _ := service(t, 1024, 2048)
	content := []byte("hello artifact")
	a, err := s.Upload(context.Background(), testOwner(), "", bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(content)
	if a.Size != int64(len(content)) || a.SHA256 != hex.EncodeToString(hash[:]) ||
		a.MIME != "text/plain; charset=utf-8" {
		t.Fatalf("unexpected metadata: %+v", a)
	}
	metadata, r, err := s.Download(context.Background(), testOwner(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := r.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, content) || metadata.ID != a.ID {
		t.Fatalf("download=%q err=%v", got, err)
	}
}
func TestUploadLimitsAndQuota(t *testing.T) {
	t.Parallel()
	s, _ := service(t, 8, 10)
	if _, uploadErr := s.Upload(
		context.Background(),
		testOwner(),
		"",
		bytes.NewBufferString("123456789"),
	); !errors.Is(
		uploadErr,
		artifacts.ErrTooLarge,
	) {
		t.Fatalf("oversize: %v", uploadErr)
	}
	if _, uploadErr := s.Upload(
		context.Background(),
		testOwner(),
		"text/plain",
		bytes.NewBufferString("12345678"),
	); uploadErr != nil {
		t.Fatal(uploadErr)
	}
	if _, uploadErr := s.Upload(
		context.Background(),
		testOwner(),
		"text/plain",
		bytes.NewBufferString("123"),
	); uploadErr == nil {
		t.Fatal("quota upload succeeded")
	}
}
func TestReadRejectsOtherOwnerAndOrganization(t *testing.T) {
	t.Parallel()
	s, _ := service(t, 10, 20)
	a, err := s.Upload(context.Background(), testOwner(), "text/plain", bytes.NewBufferString("ok"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []artifacts.Principal{{OwnerID: "other", OrgID: "org"}, {OwnerID: "user", OrgID: "other"}, {}} {
		if _, _, downloadErr := s.Download(
			context.Background(),
			p,
			a.ID,
		); !errors.Is(
			downloadErr,
			artifacts.ErrForbidden,
		) {
			t.Fatalf("unauthorized read: %v", downloadErr)
		}
	}
	for _, id := range []string{"../secret", "/etc/passwd", "..\\secret", a.ID + "/../secret"} {
		if _, _, downloadErr := s.Download(
			context.Background(),
			testOwner(),
			id,
		); !errors.Is(
			downloadErr,
			artifacts.ErrNotFound,
		) {
			t.Fatalf("path accepted %q: %v", id, downloadErr)
		}
	}
}
func TestCanceledUploadAndRead(t *testing.T) {
	t.Parallel()
	s, _ := service(t, 10, 20)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, uploadErr := s.Upload(
		ctx,
		testOwner(),
		"",
		bytes.NewBufferString("ok"),
	); !errors.Is(
		uploadErr,
		context.Canceled,
	) {
		t.Fatalf("canceled upload: %v", uploadErr)
	}
	a, err := s.Upload(context.Background(), testOwner(), "", bytes.NewBufferString("ok"))
	if err != nil {
		t.Fatal(err)
	}
	_, r, err := s.Download(ctx, testOwner(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := r.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	if _, readAllErr := io.ReadAll(r); !errors.Is(readAllErr, context.Canceled) {
		t.Fatalf("canceled read: %v", readAllErr)
	}
}
func TestLostCommitResponsePreservesUploadedContent(t *testing.T) {
	t.Parallel()
	s, store := service(t, 10, 20)
	store.ambiguous = true
	a, err := s.Upload(context.Background(), testOwner(), "", bytes.NewBufferString("ok"))
	if err != nil {
		t.Fatal(err)
	}
	_, r, err := s.Download(context.Background(), testOwner(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := r.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	got, err := io.ReadAll(r)
	if err != nil || string(got) != "ok" {
		t.Fatalf("lost committed content: %q %v", got, err)
	}
}
func TestExternalDisclosureRequiresOwnerAndExplicitDestination(t *testing.T) {
	t.Parallel()
	a := domain.Artifact{OwnerID: testOwner().OwnerID, OrgID: testOwner().OrgID, ExternalDisclosureAllowed: true}
	if allowDisclosureErr := artifacts.AllowDisclosure(
		a,
		testOwner(),
		"openai",
		[]string{"openai"},
	); allowDisclosureErr != nil {
		t.Fatal(allowDisclosureErr)
	}
	if allowDisclosureErr := artifacts.AllowDisclosure(
		a,
		testOwner(),
		"other",
		[]string{"openai"},
	); !errors.Is(
		allowDisclosureErr,
		artifacts.ErrForbidden,
	) {
		t.Fatal(allowDisclosureErr)
	}
	if allowDisclosureErr := artifacts.AllowDisclosure(
		a,
		artifacts.Principal{OwnerID: "other", OrgID: "org"},
		"openai",
		[]string{"openai"},
	); !errors.Is(
		allowDisclosureErr,
		artifacts.ErrForbidden,
	) {
		t.Fatal(allowDisclosureErr)
	}
}

func TestReadRejectsReplacedSymlink(t *testing.T) {
	t.Parallel()
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
		if closeErr := s.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	a, err := s.Upload(context.Background(), testOwner(), "", bytes.NewBufferString("ok"))
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(volume, a.ID)
	if removeErr := os.Remove(target); removeErr != nil {
		t.Fatal(removeErr)
	}
	if symlinkErr := os.Symlink("../secret", target); symlinkErr != nil {
		t.Fatal(symlinkErr)
	}
	if _, _, downloadErr := s.Download(
		context.Background(),
		testOwner(),
		a.ID,
	); !errors.Is(
		downloadErr,
		artifacts.ErrInvalid,
	) {
		t.Fatalf("symlink accepted: %v", downloadErr)
	}
}
func TestConcurrentUploadsCannotExceedQuota(t *testing.T) {
	t.Parallel()
	s, _ := service(t, 10, 10)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			_, uploadErr := s.Upload(context.Background(), testOwner(), "", bytes.NewBufferString("12345678"))
			results <- uploadErr
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
	t.Parallel()
	s, _ := service(t, 1024, 4096)
	var content bytes.Buffer
	if encodeErr := png.Encode(&content, image.NewRGBA(image.Rect(0, 0, 1, 1))); encodeErr != nil {
		t.Fatal(encodeErr)
	}
	a, err := s.Upload(context.Background(), testOwner(), "image/png", bytes.NewReader(content.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	image, err := s.ForModel(context.Background(), testOwner(), a.ID, "openai", []string{"openai"}, true, 1024)
	if err != nil || !bytes.Equal(image.Bytes, content.Bytes()) {
		t.Fatalf("forward image: %v", err)
	}
	if _, forModelErr := s.ForModel(
		context.Background(),
		testOwner(),
		a.ID,
		"openai",
		nil,
		true,
		1024,
	); !errors.Is(
		forModelErr,
		artifacts.ErrForbidden,
	) {
		t.Fatalf("disclosure not denied: %v", forModelErr)
	}
	if _, forModelErr := s.ForModel(
		context.Background(),
		testOwner(),
		a.ID,
		"openai",
		[]string{"openai"},
		false,
		1024,
	); !errors.Is(
		forModelErr,
		artifacts.ErrForbidden,
	) {
		t.Fatalf("capability not denied: %v", forModelErr)
	}
	if _, forModelErr := s.ForModel(
		context.Background(),
		testOwner(),
		a.ID,
		"openai",
		[]string{"openai"},
		true,
		1,
	); !errors.Is(
		forModelErr,
		artifacts.ErrTooLarge,
	) {
		t.Fatalf("size not denied: %v", forModelErr)
	}
	forged, err := s.Upload(
		context.Background(),
		testOwner(),
		"image/png",
		bytes.NewBufferString("this is text"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, forModelErr := s.ForModel(
		context.Background(),
		testOwner(),
		forged.ID,
		"openai",
		[]string{"openai"},
		true,
		1024,
	); !errors.Is(
		forModelErr,
		artifacts.ErrInvalid,
	) {
		t.Fatalf("forged MIME not denied: %v", forModelErr)
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
	t.Parallel()
	s, store := service(t, 10, 100)
	entered := make(chan struct{}, 5)
	release := make(chan struct{})
	activeCtx, cancelActive := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			_, uploadErr := s.Upload(
				activeCtx,
				testOwner(),
				"",
				&blockedUploadInput{entered: entered, release: release},
			)
			if !errors.Is(uploadErr, context.Canceled) {
				t.Errorf("active canceled upload: %v", uploadErr)
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
		_, uploadErr := s.Upload(queuedCtx, testOwner(), "", &blockedUploadInput{entered: entered, release: release})
		result <- uploadErr
	}()
	cancelQueued()
	select {
	case operationErr := <-result:
		if !errors.Is(operationErr, context.Canceled) {
			t.Fatalf("queued cancellation: %v", operationErr)
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
