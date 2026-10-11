package api

import (
	"context"
	"net/http"

	"github.com/complynx/clx-scripter/internal/domain"
)

func (s *server) createArtifactHandle(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var input struct {
		ArtifactID string `json:"artifact_id"`
	}
	if err := s.decode(w, r, &input); err != nil {
		return err
	}
	handle, err := s.store.CreateArtifactHandle(r.Context(), p, r.PathValue("id"), input.ArtifactID)
	if err == nil {
		write(w, http.StatusCreated, handle)
	}
	return err
}
func (s *server) handleContent(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	handle, err := s.store.LoadArtifactHandle(r.Context(), p, r.PathValue("id"))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithDeadline(r.Context(), handle.ExpiresAt)
	defer cancel()
	r = r.WithContext(ctx)
	r.SetPathValue("id", handle.ArtifactID)
	return s.artifactContent(w, r, p)
}
