package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"

	"github.com/complynx/clx-scripter/internal/domain"
)

type streamState struct {
	count      int
	generation uint64
	timer      *time.Timer
}
type streamTracker struct {
	mu   sync.Mutex
	runs map[string]*streamState
}

func (s *server) subscribe(requestContext context.Context, lifecycleContext context.Context, r *http.Request, p domain.Principal, run domain.Run) func() {
	s.streams.mu.Lock()
	state := s.streams.runs[run.ID]
	if state == nil {
		state = &streamState{}
		s.streams.runs[run.ID] = state
	}
	if state.timer != nil {
		state.timer.Stop()
		state.timer = nil
	}
	state.count++
	state.generation++
	s.streams.mu.Unlock()
	return func() {
		s.streams.mu.Lock()
		defer s.streams.mu.Unlock()
		state.count--
		state.generation++
		if state.count > 0 {
			return
		}
		if run.DisconnectPolicy != "pause" {
			delete(s.streams.runs, run.ID)
			return
		}
		generation := state.generation
		state.timer = time.AfterFunc(s.options.DisconnectGrace, func() {
			// Request context has ended. Reauthorize with a bounded lifecycle context.
			ctx, cancel := context.WithTimeout(context.WithoutCancel(requestContext), 2*time.Second)
			defer cancel()
			stop := context.AfterFunc(lifecycleContext, cancel)
			defer stop()
			if lifecycleContext.Err() != nil || ctx.Err() != nil {
				return
			}
			var fresh domain.Principal
			var err error
			if s.options.AuthorizeExecution != nil {
				fresh, err = s.options.AuthorizeExecution(ctx, run)
			} else {
				fresh, err = s.auth.Authenticate(ctx, r)
			}
			if err != nil || fresh.OrgID != run.OrgID || defaultString(fresh.Actor, fresh.Subject) != run.ActorID || !fresh.Has("runs.control") || (!fresh.ExpiresAt.IsZero() && !time.Now().Before(fresh.ExpiresAt)) {
				s.clearStream(run.ID, state, generation)
				return
			}
			s.streams.mu.Lock()
			current := s.streams.runs[run.ID]
			if current != state || state.count != 0 || state.generation != generation {
				s.streams.mu.Unlock()
				return
			}
			delete(s.streams.runs, run.ID)
			s.streams.mu.Unlock()
			var token [16]byte
			if _, err := rand.Read(token[:]); err != nil {
				return
			}
			_, _ = s.store.Control(ctx, fresh, run.ID, "disconnect:"+hex.EncodeToString(token[:]), "pause")
		})
	}
}
func (s *server) clearStream(id string, state *streamState, generation uint64) {
	s.streams.mu.Lock()
	defer s.streams.mu.Unlock()
	if current := s.streams.runs[id]; current == state && state.count == 0 && state.generation == generation {
		delete(s.streams.runs, id)
	}
}
