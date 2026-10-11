package artifacts

import (
	"context"
	"errors"
	"os"

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
		if contextErr := ctx.Err(); contextErr != nil {
			return collected, contextErr
		}
		id, valid := compactArtifactID(artifact.ID)
		if !valid {
			return collected, ErrInvalid
		}
		artifact.ID = id
		removeErr := s.root.Remove(artifact.ID)
		if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return collected, removeErr
		}
		if directorySyncErr := s.syncDirectory(); directorySyncErr != nil {
			return collected, directorySyncErr
		}

		if markArtifactCollectedErr := collector.MarkArtifactCollected(
			ctx,
			artifact.ID,
		); markArtifactCollectedErr != nil {
			return collected, markArtifactCollectedErr
		}
		collected++
	}
	return collected, nil
}
