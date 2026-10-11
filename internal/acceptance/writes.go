package acceptance

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

func (s *Service) persist() error {
	raw, err := json.Marshal(s.operations)
	if err != nil {
		return err
	}
	if len(raw) > maxDocumentBytes {
		return errors.New("QA state exceeds limit")
	}
	file, err := os.CreateTemp(filepath.Dir(s.path), "qa-state-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), s.path)
}
func (s *Service) prepare(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r, true) {
		return
	}
	var in envelope
	if !decode(w, r, &in) {
		return
	}
	normalized, err := normalizedArguments(in.Arguments)
	if err != nil || in.Protocol != 1 || in.ID == "" || in.RunID == "" {
		http.Error(w, "Invalid preparation", http.StatusUnprocessableEntity)
		return
	}
	in.Arguments = normalized
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, found := s.operations[in.ID]
	if found &&
		(previous.Actor != identity(r) || previous.RunID != in.RunID || string(previous.Arguments) != string(in.Arguments)) {
		http.Error(w, "Operation binding conflict", http.StatusConflict)
		return
	}
	if !found {
		if len(s.operations) >= maxOperations {
			http.Error(w, "QA state full", http.StatusTooManyRequests)
			return
		}
		previous = record{
			Actor:        identity(r),
			RunID:        in.RunID,
			Arguments:    in.Arguments,
			Confirmation: uuid.NewString(),
			Expires:      time.Now().Add(prepareTTL),
		}
		s.operations[in.ID] = previous
		if persistErr := s.persist(); persistErr != nil {
			delete(s.operations, in.ID)
			http.Error(w, "State unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	respond(
		w,
		map[string]any{
			fieldStatus:       "prepared",
			fieldOperationID:  in.ID,
			"confirmation_id": previous.Confirmation,
			"arguments":       previous.Arguments,
			"expires_at":      previous.Expires,
			"description":     "Apply fixture value once",
		},
	)
}
func (s *Service) write(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r, true) {
		return
	}
	var in envelope
	if !decode(w, r, &in) {
		return
	}
	normalized, err := normalizedArguments(in.Arguments)
	if err != nil {
		http.Error(w, "Invalid arguments", http.StatusUnprocessableEntity)
		return
	}
	in.Arguments = normalized
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, found := s.operations[in.ID]
	if !found || in.Protocol != 1 || in.IdempotencyKey != in.ID || in.Confirmation.Mode != "service_id" ||
		in.Confirmation.ID != previous.Confirmation ||
		previous.Actor != identity(r) ||
		previous.RunID != in.RunID ||
		string(previous.Arguments) != string(in.Arguments) {
		http.Error(w, "Confirmation denied", http.StatusConflict)
		return
	}
	if !previous.Applied {
		if !time.Now().Before(previous.Expires) {
			http.Error(w, "Preparation expired", http.StatusConflict)
			return
		}
		next := previous
		next.Applied = true
		s.operations[in.ID] = next
		if persistErr := s.persist(); persistErr != nil {
			s.operations[in.ID] = previous
			http.Error(w, "State unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	if s.dropNext {
		s.dropNext = false
		if hijacker, ok := w.(http.Hijacker); ok {
			connection, _, hijackErr := hijacker.Hijack()
			if hijackErr == nil {
				_ = connection.Close()
				return
			}
		}
		http.Error(w, "Reply lost after commit", http.StatusServiceUnavailable)
		return
	}
	value, _ := arguments(previous.Arguments)
	respond(
		w,
		map[string]any{
			fieldStatus:      stateSucceeded,
			fieldOperationID: in.ID,
			fieldResult:      map[string]string{fieldValue: value},
		},
	)
}
func (s *Service) status(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r, false) {
		return
	}
	var in envelope
	if !decode(w, r, &in) {
		return
	}
	if in.Protocol != 1 || in.ID == "" {
		http.Error(w, "Invalid status request", http.StatusUnprocessableEntity)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, found := s.operations[in.ID]
	state := "not_found"
	if found && previous.Actor == identity(r) && previous.Applied {
		state = stateSucceeded
	}
	value, _ := arguments(previous.Arguments)
	if state != stateSucceeded {
		value = ""
	}
	respond(
		w,
		map[string]any{fieldStatus: state, fieldOperationID: in.ID, fieldResult: map[string]string{fieldValue: value}},
	)
}
func (s *Service) faults(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r, true) {
		return
	}
	var input struct {
		DropNext bool `json:"drop_next_write_reply"`
	}
	if !decode(w, r, &input) {
		return
	}
	s.mu.Lock()
	s.dropNext = input.DropNext
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}
