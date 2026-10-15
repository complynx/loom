package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"

	"github.com/complynx/loom/internal/domain"
)

type streamState struct {
	control    sync.Mutex
	cancel     context.CancelFunc
	count      int
	generation uint64
	timer      *time.Timer
}
type streamTracker struct {
	mu   sync.Mutex
	runs map[string]*streamState
}

const disconnectAuthorizationTimeout = 2 * time.Second

func (s *server) subscribe(
	requestContext context.Context,
	lifecycleContext context.Context,
	r *http.Request,
	_ domain.Principal,
	run domain.Run,
) func() {
	state := s.subscribeState(run.ID)
	return func() {
		s.streams.mu.Lock()
		defer s.streams.mu.Unlock()
		state.count--
		state.generation++
		if state.count > 0 {
			return
		}
		if run.DisconnectPolicy != pauseCommand {
			delete(s.streams.runs, run.ID)
			return
		}
		generation := state.generation
		state.timer = time.AfterFunc(s.options.DisconnectGrace, func() {
			s.pauseDisconnected(requestContext, lifecycleContext, r, run, state, generation)
		})
	}
}

func (s *server) pauseDisconnected(
	requestContext, lifecycleContext context.Context,
	r *http.Request,
	run domain.Run,
	state *streamState,
	generation uint64,
) {
	// Request context has ended. Reauthorize with a bounded lifecycle context.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(requestContext), disconnectAuthorizationTimeout)
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
	if err != nil || fresh.OrgID != run.OrgID || defaultString(fresh.Actor, fresh.Subject) != run.ActorID ||
		!fresh.Has("runs.control") || (!fresh.ExpiresAt.IsZero() && !time.Now().Before(fresh.ExpiresAt)) {
		s.clearStream(run.ID, state, generation)
		return
	}
	// Serialize the final pause with this run's subscriptions. A reconnect can
	// cancel a database wait before it waits for this gate itself.
	state.control.Lock()
	defer state.control.Unlock()
	s.streams.mu.Lock()
	current := s.streams.runs[run.ID]
	if current != state || state.count != 0 || state.generation != generation {
		s.streams.mu.Unlock()
		return
	}
	state.cancel = cancel
	s.streams.mu.Unlock()
	defer s.clearStream(run.ID, state, generation)
	var token [16]byte
	if _, randomErr := rand.Read(token[:]); randomErr != nil {
		return
	}
	_, _ = s.store.Control(ctx, fresh, run.ID, "disconnect:"+hex.EncodeToString(token[:]), pauseCommand)
}

// subscribeState cancels an in-flight disconnect pause before registering the
// subscriber. The per-run gate keeps a completed pause before registration.
func (s *server) subscribeState(id string) *streamState {
	for {
		s.streams.mu.Lock()
		state := s.streams.runs[id]
		if state == nil {
			state = &streamState{}
			s.streams.runs[id] = state
		}
		// Invalidate the old timer before waiting for the per-run gate.
		state.generation++
		if state.cancel != nil {
			state.cancel()
		}
		s.streams.mu.Unlock()
		state.control.Lock()
		s.streams.mu.Lock()
		if s.streams.runs[id] != state {
			s.streams.mu.Unlock()
			state.control.Unlock()
			continue
		}
		if state.timer != nil {
			state.timer.Stop()
			state.timer = nil
		}
		state.cancel = nil
		state.count++
		state.generation++
		s.streams.mu.Unlock()
		state.control.Unlock()
		return state
	}
}
func (s *server) clearStream(id string, state *streamState, generation uint64) {
	s.streams.mu.Lock()
	defer s.streams.mu.Unlock()
	if current := s.streams.runs[id]; current == state && state.count == 0 && state.generation == generation {
		delete(s.streams.runs, id)
	}
}
