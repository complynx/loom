package api

import (
	"net/http"

	"github.com/complynx/loom/internal/domain"
)

func (s *server) setReaction(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var body domain.SetReaction
	if err := s.decode(w, r, &body); err != nil {
		return err
	}
	event, err := s.store.SetReaction(r.Context(), p, r.PathValue("id"), body)
	if err == nil {
		write(w, http.StatusCreated, event)
	}
	return err
}
