package api

import (
	"net/http"

	"github.com/complynx/clx-scripter/internal/domain"
)

func (s *server) readHistory(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var in domain.ReadHistoryRequest
	if err := s.decode(w, r, &in); err != nil {
		return err
	}
	out, err := s.store.ReadHistory(r.Context(), p, r.PathValue("id"), in)
	if err == nil {
		write(w, http.StatusOK, out)
	}
	return err
}
func (s *server) indexEmbedding(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	var in domain.EventEmbedding
	if err := s.decode(w, r, &in); err != nil {
		return err
	}
	if err := s.store.IndexEmbedding(r.Context(), p, r.PathValue("id"), in); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
