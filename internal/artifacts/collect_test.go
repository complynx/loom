package artifacts_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/complynx/clx-scripter/internal/artifacts"
	"github.com/complynx/clx-scripter/internal/domain"
)

type collectorStore struct {
	*metadataStore
	deleted      map[string]bool
	collected    map[string]bool
	failNextMark bool
}

func (m *collectorStore) CommitArtifact(_ context.Context, a domain.Artifact, quota int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var used int64
	for id, row := range m.rows {
		if row.OwnerID == a.OwnerID && !m.collected[id] {
			used += row.Size
		}
	}
	if used+a.Size > quota {
		return errors.New("quota exceeded")
	}
	m.rows[a.ID] = a
	return nil
}
func (m *collectorStore) GetArtifact(ctx context.Context, id string) (domain.Artifact, error) {
	m.mu.Lock()
	deleted := m.deleted[id]
	m.mu.Unlock()
	if deleted {
		return domain.Artifact{}, domain.ErrNotFound
	}
	return m.metadataStore.GetArtifact(ctx, id)
}
func (m *collectorStore) ListDeletedArtifacts(_ context.Context, limit int) ([]domain.Artifact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := []domain.Artifact{}
	for id, row := range m.rows {
		if m.deleted[id] && !m.collected[id] && len(result) < limit {
			result = append(result, row)
		}
	}
	return result, nil
}
func (m *collectorStore) MarkArtifactCollected(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failNextMark {
		m.failNextMark = false
		return errors.New("database response lost")
	}
	if !m.deleted[id] {
		return artifacts.ErrForbidden
	}
	m.collected[id] = true
	return nil
}
func (m *collectorStore) deleteArtifact(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleted[id] = true
}
func collectibleService(t *testing.T) (*artifacts.Service, *collectorStore) {
	t.Helper()
	store := &collectorStore{metadataStore: &metadataStore{rows: make(map[string]domain.Artifact)}, deleted: make(map[string]bool), collected: make(map[string]bool)}
	s, err := artifacts.New(t.TempDir(), store, artifacts.Limits{MaxUploadBytes: 10, QuotaBytes: 10})
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
func TestDeletedArtifactRetainsQuotaUntilPhysicalCollection(t *testing.T) {
	s, store := collectibleService(t)
	ctx := context.Background()
	a, err := s.Upload(ctx, owner, "", bytes.NewBufferString("12345678"))
	if err != nil {
		t.Fatal(err)
	}
	store.deleteArtifact(a.ID)
	if _, err := s.Upload(ctx, owner, "", bytes.NewBufferString("123")); err == nil {
		t.Fatal("tombstoned blob prematurely released quota")
	}
	if _, _, err := s.Download(ctx, owner, a.ID); !errors.Is(err, artifacts.ErrNotFound) {
		t.Fatalf("deleted handle readable: %v", err)
	}
	count, err := s.CollectDeleted(ctx, 10)
	if err != nil || count != 1 {
		t.Fatalf("collection=%d error=%v", count, err)
	}
	replacement, err := s.Upload(ctx, owner, "", bytes.NewBufferString("123"))
	if err != nil {
		t.Fatalf("physical collection did not release quota: %v", err)
	}
	if replacement.ID == a.ID {
		t.Fatal("replacement reused deleted artifact ID")
	}
	count, err = s.CollectDeleted(ctx, 10)
	if err != nil || count != 0 {
		t.Fatalf("repeat collection=%d error=%v", count, err)
	}
}
func TestPhysicalDeletionRetryPreservesQuotaAndDifferentArtifact(t *testing.T) {
	s, store := collectibleService(t)
	ctx := context.Background()
	removed, err := s.Upload(ctx, owner, "", bytes.NewBufferString("12345678"))
	if err != nil {
		t.Fatal(err)
	}
	store.deleteArtifact(removed.ID)
	store.failNextMark = true
	if _, err := s.CollectDeleted(ctx, 10); err == nil {
		t.Fatal("lost collection metadata response was hidden")
	}
	if _, err := s.Upload(ctx, owner, "", bytes.NewBufferString("123")); err == nil {
		t.Fatal("failed marker released quota")
	}
	count, err := s.CollectDeleted(ctx, 10)
	if err != nil || count != 1 {
		t.Fatalf("absent-file retry=%d %v", count, err)
	}
	live, err := s.Upload(ctx, owner, "", bytes.NewBufferString("123"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CollectDeleted(ctx, 10); err != nil {
		t.Fatal(err)
	}
	_, reader, err := s.Download(ctx, owner, live.ID)
	if err != nil {
		t.Fatalf("collection removed different live artifact: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
}
