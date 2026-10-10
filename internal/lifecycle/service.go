// Package lifecycle applies durable deadlines without executing agent actions.
package lifecycle

import (
	"context"
	"github.com/complynx/clx-scripter/internal/store"
	"log/slog"
	"time"
)

type Service struct {
	Store    *store.Store
	Interval time.Duration
	Batch    int
	Logger   *slog.Logger
}

func New(s *store.Store, interval time.Duration) *Service {
	if interval <= 0 {
		interval = time.Minute
	}
	return &Service{Store: s, Interval: interval, Batch: 100, Logger: slog.Default()}
}

// Run performs one initial sweep and repeats until shutdown. Errors are logged
// without payloads; the next sweep retries only durable database transitions.
func (s *Service) Run(ctx context.Context) {
	timer := time.NewTicker(s.Interval)
	defer timer.Stop()
	for {
		if _, e := s.Store.SweepLifecycle(ctx, s.Batch); e != nil && ctx.Err() == nil {
			s.Logger.Error("lifecycle sweep failed", "error_code", "dependency_unavailable")
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
