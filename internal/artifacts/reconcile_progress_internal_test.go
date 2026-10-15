package artifacts

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/complynx/loom/internal/domain"
)

type reconcileStore struct {
	live    map[string]bool
	delay   time.Duration
	failure error
}

func (s *reconcileStore) CommitArtifact(context.Context, domain.Artifact, int64) error {
	return nil
}

func (s *reconcileStore) GetArtifact(ctx context.Context, id string) (domain.Artifact, error) {
	if s.failure != nil {
		return domain.Artifact{}, s.failure
	}
	if s.delay > 0 {
		timer := time.NewTimer(s.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return domain.Artifact{}, ctx.Err()
		case <-timer.C:
		}
	}
	if s.live[id] {
		return domain.Artifact{ID: id}, nil
	}
	return domain.Artifact{}, domain.ErrNotFound
}

func agedReconcileFile(t *testing.T, path, name string) {
	t.Helper()
	filename := filepath.Join(path, name)
	if err := os.WriteFile(filename, []byte("blob"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filename, old, old); err != nil {
		t.Fatal(err)
	}
}

func reconcileService(t *testing.T, path string, store *reconcileStore) *Service {
	t.Helper()
	service, err := New(path, store, Limits{MaxUploadBytes: 1024, QuotaBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := service.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	return service
}

func TestReconcileShortPassesReachOrphanBeyondLivePrefix(t *testing.T) {
	t.Parallel()
	path := t.TempDir()
	store := &reconcileStore{live: make(map[string]bool), delay: 2 * time.Millisecond}
	for i := range 32 {
		name := fmt.Sprintf("%032x", i)
		agedReconcileFile(t, path, name)
		store.live[name] = true
	}
	// Choose the last entry in the filesystem's enumeration, not lexical order.
	dir, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	entries, readErr := dir.ReadDir(-1)
	if closeErr := dir.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	orphan := entries[len(entries)-1].Name()
	delete(store.live, orphan)
	service := reconcileService(t, path, store)
	removed := 0
	for range 64 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		count, reconcileErr := service.Reconcile(ctx, time.Hour)
		cancel()
		removed += count
		if reconcileErr != nil && !errors.Is(reconcileErr, context.DeadlineExceeded) {
			t.Fatal(reconcileErr)
		}
		if removed != 0 {
			break
		}
	}
	if removed != 1 {
		t.Fatalf("short reconciliation passes removed %d orphans, want 1", removed)
	}
	if _, statErr := os.Stat(filepath.Join(path, orphan)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("orphan remains: %v", statErr)
	}
	for live := range store.live {
		if _, statErr := os.Stat(filepath.Join(path, live)); statErr != nil {
			t.Fatalf("live artifact %s lost: %v", live, statErr)
		}
	}
}

func TestReconcileRetriesUncertainEntryAndRescansAfterEOF(t *testing.T) {
	t.Parallel()
	path := t.TempDir()
	name := fmt.Sprintf("%032x", 1)
	agedReconcileFile(t, path, name)
	unavailable := errors.New("metadata unavailable")
	store := &reconcileStore{failure: unavailable}
	service := reconcileService(t, path, store)
	if count, err := service.Reconcile(context.Background(), time.Hour); count != 0 || !errors.Is(err, unavailable) {
		t.Fatalf("uncertain lookup: removed=%d err=%v", count, err)
	}
	if _, err := os.Stat(filepath.Join(path, name)); err != nil {
		t.Fatalf("uncertain content lost: %v", err)
	}
	store.failure = nil
	if count, err := service.Reconcile(context.Background(), time.Hour); count != 1 || err != nil {
		t.Fatalf("retry current entry: removed=%d err=%v", count, err)
	}
	agedReconcileFile(t, path, name)
	if count, err := service.Reconcile(context.Background(), time.Hour); count != 1 || err != nil {
		t.Fatalf("new scan after EOF: removed=%d err=%v", count, err)
	}
}

func TestReconcileCloseReleasesPausedDirectory(t *testing.T) {
	t.Parallel()
	path := t.TempDir()
	agedReconcileFile(t, path, fmt.Sprintf("%032x", 1))
	store := &reconcileStore{failure: context.DeadlineExceeded}
	service, err := New(path, store, Limits{MaxUploadBytes: 1024, QuotaBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if _, reconcileErr := service.Reconcile(
		context.Background(),
		time.Hour,
	); !errors.Is(
		reconcileErr,
		context.DeadlineExceeded,
	) {
		t.Fatal(reconcileErr)
	}
	directory := service.scanDir
	if directory == nil {
		t.Fatal("paused scan has no directory")
	}
	if closeErr := service.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if _, statErr := directory.Read(make([]byte, 1)); !errors.Is(statErr, os.ErrClosed) {
		t.Fatalf("directory cursor remains open: %v", statErr)
	}
}
