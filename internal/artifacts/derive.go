package artifacts

import (
	"context"
	"io"

	"github.com/complynx/loom/internal/domain"
)

// DeriveToChat preserves the source disclosure policy and commits the chat reference atomically.
func (s *Service) DeriveToChat(
	ctx context.Context,
	p domain.Principal,
	ownerID, chatID, sourceID, mimeType string,
	input io.Reader,
) (domain.Artifact, error) {
	source, err := s.Metadata(ctx, Principal{OwnerID: ownerID, OrgID: p.OrgID}, sourceID)
	if err != nil {
		return domain.Artifact{}, err
	}
	repository, ok := s.store.(interface {
		CommitChatArtifact(context.Context, domain.Principal, string, domain.Artifact, int64) error
	})
	if !ok {
		return domain.Artifact{}, ErrInvalid
	}
	item, err := s.upload(
		ctx,
		Principal{OwnerID: ownerID, OrgID: p.OrgID},
		mimeType,
		input,
		source.ExternalDisclosureAllowed,
		source.SourceServiceID,
		func(c context.Context, a domain.Artifact, quota int64) error {
			if source.ExpiresAt.Before(a.ExpiresAt) && !source.ExpiresAt.IsZero() {
				a.ExpiresAt = source.ExpiresAt
			}
			return repository.CommitChatArtifact(c, p, chatID, a, quota)
		},
	)
	if !source.ExpiresAt.IsZero() && source.ExpiresAt.Before(item.ExpiresAt) {
		item.ExpiresAt = source.ExpiresAt
	}
	return item, err
}
