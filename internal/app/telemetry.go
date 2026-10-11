package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/riverqueue/river"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/identity"
	"github.com/complynx/loom/internal/modules"
	"github.com/complynx/loom/internal/provider"
	"github.com/complynx/loom/internal/runs"
	"github.com/complynx/loom/internal/telemetry"
)

func operationOutcome(err error) string {
	_, snoozed := errors.AsType[*river.JobSnoozeError](err)
	if err == nil || snoozed {
		return "success"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var failure *provider.Failure
	if errors.As(err, &failure) && failure.UnknownUsage {
		return outcomeUnknown
	}
	if errors.Is(err, identity.ErrUnauthorized) || errors.Is(err, modules.ErrForbidden) {
		return "denied"
	}
	var suspended *runs.Suspension
	if errors.As(err, &suspended) && suspended.State == domain.Reconciling {
		return outcomeUnknown
	}
	var denied *domain.Error
	if errors.As(err, &denied) && (denied.Status == http.StatusUnauthorized || denied.Status == http.StatusForbidden) {
		return "denied"
	}
	return "error"
}

type measuredProvider struct {
	client   provider.Client
	observer *telemetry.Telemetry
}

func (p measuredProvider) SupportsCompaction(request provider.Request) bool {
	if capable, ok := p.client.(interface{ SupportsCompaction(provider.Request) bool }); ok {
		return capable.SupportsCompaction(request)
	}
	_, ok := p.client.(provider.CompactClient)
	return ok
}

func (p measuredProvider) Compact(ctx context.Context, request provider.Request) (provider.Response, error) {
	started := time.Now()
	client, ok := p.client.(provider.CompactClient)
	if !ok {
		return provider.Response{}, &provider.Failure{Code: "compaction_unavailable"}
	}
	result, err := client.Compact(ctx, request)
	p.observer.Operation(ctx, "model", operationOutcome(err), telemetry.RunID(ctx), started)
	return result, err
}

func (p measuredProvider) Respond(ctx context.Context, request provider.Request) (provider.Response, error) {
	started := time.Now()
	var err error
	defer func() { p.observer.Operation(ctx, "model", operationOutcome(err), telemetry.RunID(ctx), started) }()
	p.observer.DebugPayload(ctx, "model_request", request)
	result, err := p.client.Respond(ctx, request)
	p.observer.DebugPayload(ctx, "model_response", result)
	return result, err
}
func (p measuredProvider) CountInputTokens(ctx context.Context, request provider.Request) (int64, error) {
	started := time.Now()
	var err error
	defer func() { p.observer.Operation(ctx, "token_count", operationOutcome(err), telemetry.RunID(ctx), started) }()
	counter, ok := p.client.(provider.TokenCounter)
	if !ok {
		err = errors.New("token counting unavailable")
		return 0, err
	}
	result, err := counter.CountInputTokens(ctx, request)
	return result, err
}

type measuredAuth struct {
	auth     *identity.Authenticator
	observer *telemetry.Telemetry
}

func (a measuredAuth) Authenticate(ctx context.Context, r *http.Request) (domain.Principal, error) {
	started := time.Now()
	var err error
	defer func() {
		a.observer.Operation(ctx, "authorization", operationOutcome(err), telemetry.RunID(ctx), started)
	}()
	principal, err := a.auth.Authenticate(ctx, r)
	return principal, err
}

// River attributes can include job arguments and raw dependency errors. Keep its
// severity and a fixed message only; application logs supply safe run diagnostics.
type queueLogHandler struct{ next slog.Handler }

func (h queueLogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}
func (h queueLogHandler) Handle(ctx context.Context, record slog.Record) error {
	clean := slog.NewRecord(record.Time, record.Level, "queue event", 0)
	clean.AddAttrs(slog.String("component", "queue"))
	return h.next.Handle(ctx, clean)
}
func (h queueLogHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h queueLogHandler) WithGroup(_ string) slog.Handler      { return h }
