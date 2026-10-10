package artifacts

import (
	"context"
	"errors"
	"os"
	"runtime"

	"github.com/complynx/clx-scripter/internal/domain"
)

// MetadataCollector lists only immutable tombstones. Quota must count every
// uncollected artifact, including deleted artifacts, until MarkArtifactCollected
// succeeds after durable physical removal. Deleted artifacts cannot be reattached.
type MetadataCollector interface {
	ListDeletedArtifacts(context.Context, int) ([]domain.Artifact, error)
	MarkArtifactCollected(context.Context, string) error
}

// CollectDeleted releases disk-backed quota immediately for tombstoned blobs.
// A lost database response is safe to retry: an already absent blob is collected
// again without touching any different artifact or releasing quota prematurely.
func (s *Service) CollectDeleted(ctx context.Context, limit int) (int, error) {
	collector, ok := s.store.(MetadataCollector)
	if !ok || limit < 1 || limit > 1000 {
		return 0, ErrInvalid
	}
	s.activity.Lock()
	defer s.activity.Unlock()
	artifacts, err := collector.ListDeletedArtifacts(ctx, limit)
	if err != nil {
		return 0, err
	}
	if len(artifacts) > limit {
		return 0, ErrInvalid
	}
	collected := 0
	for _, artifact := range artifacts {
		if err := ctx.Err(); err != nil {
			return collected, err
		}
		if !validID(artifact.ID) {
			return collected, ErrInvalid
		}
		err := s.root.Remove(artifact.ID)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return collected, err
		}
		if runtime.GOOS == "linux" {
			directory, err := s.root.Open(".")
			if err != nil {
				return collected, err
			}
			syncErr := directory.Sync()
			closeErr := directory.Close()
			if syncErr != nil {
				return collected, syncErr
			}
			if closeErr != nil {
				return collected, closeErr
			}
		}
		if err := collector.MarkArtifactCollected(ctx, artifact.ID); err != nil {
			return collected, err
		}
		collected++
	}
	return collected, nil
}
