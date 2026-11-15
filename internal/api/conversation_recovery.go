package api

import (
	"net/http"

	"github.com/complynx/loom/internal/domain"
)

func (s *server) abandonChatMessage(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	k, err := key(r)
	if err != nil {
		return err
	}
	if r.ContentLength != 0 {
		var body struct{}
		if err = s.decode(w, r, &body); err != nil {
			return err
		}
	}
	v, err := s.store.AbandonChatMessage(r.Context(), p, r.PathValue("id"), r.PathValue("message"), k)
	if err == nil {
		write(w, http.StatusOK, v)
	}
	return err
}

func (s *server) activeChatRun(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	v, err := s.store.ActiveChatRun(r.Context(), p, r.PathValue("id"))
	if err == nil {
		write(w, http.StatusOK, v)
	}
	return err
}

func (s *server) recoverChatMessage(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	k, err := key(r)
	if err != nil {
		return err
	}
	var body domain.RecoverMessage
	if err = s.decode(w, r, &body); err != nil {
		return err
	}
	v, err := s.store.RecoverChatMessage(r.Context(), p, r.PathValue("id"), k, body)
	if err != nil {
		return err
	}
	if v.RunID != "" {
		w.Header().Set("Location", "/v1/runs/"+v.RunID)
	}
	write(w, http.StatusAccepted, v)
	return nil
}
