package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	ctx, finishAction := p.beginModelAction(ctx, "model.compact", request)
	ctx, finishTrace := p.observer.Start(ctx, "model", telemetry.RunID(ctx))
	started := time.Now()
	client, ok := p.client.(provider.CompactClient)
	if !ok {
		err := &provider.Failure{Code: "compaction_unavailable"}
		finishTrace(err)
		finishAction(nil, err)
		return provider.Response{}, err
	}
	result, err := client.Compact(ctx, request)
	finishAction(result, err)
	finishTrace(err)
	p.observer.Operation(ctx, "model", operationOutcome(err), telemetry.RunID(ctx), started)
	return result, err
}

func (p measuredProvider) Respond(ctx context.Context, request provider.Request) (provider.Response, error) {
	ctx, finishAction := p.beginModelAction(ctx, "model.respond", request)
	ctx, finishTrace := p.observer.Start(ctx, "model", telemetry.RunID(ctx))
	started := time.Now()
	var err error
	defer func() { finishTrace(err) }()
	defer func() { p.observer.Operation(ctx, "model", operationOutcome(err), telemetry.RunID(ctx), started) }()
	p.observer.DebugPayload(ctx, "model_request", request)
	result, err := p.client.Respond(ctx, request)
	finishAction(result, err)
	p.observer.DebugPayload(ctx, "model_response", result)
	return result, err
}
func (p measuredProvider) CountInputTokens(ctx context.Context, request provider.Request) (int64, error) {
	ctx, finishAction := p.beginModelAction(ctx, "model.count_tokens", request)
	ctx, finishTrace := p.observer.Start(ctx, "token_count", telemetry.RunID(ctx))
	started := time.Now()
	var err error
	defer func() { finishTrace(err) }()
	defer func() { p.observer.Operation(ctx, "token_count", operationOutcome(err), telemetry.RunID(ctx), started) }()
	counter, ok := p.client.(provider.TokenCounter)
	if !ok {
		err = errors.New("token counting unavailable")
		finishAction(nil, err)
		return 0, err
	}
	result, err := counter.CountInputTokens(ctx, request)
	finishAction(map[string]any{"count": result}, err)
	return result, err
}

// Record the effective instructions/contracts, but reference history and checkpoint
// items by their ordered hashes. This avoids copying the full history on each call.
func (p measuredProvider) beginModelAction(
	ctx context.Context,
	kind string,
	request provider.Request,
) (context.Context, func(any, error)) {
	if !p.observer.ActionsEnabled() {
		return ctx, func(any, error) {}
	}
	manifest := make([]modelInputReference, 0, len(request.Input))
	for _, item := range request.Input {
		var entry modelInputReference
		_ = json.Unmarshal(item, &entry)
		hash := sha256.Sum256(item)
		entry.SHA256, entry.Bytes = hex.EncodeToString(hash[:]), len(item)
		manifest = append(manifest, entry)
	}
	ctx, finish := p.observer.BeginAction(ctx, kind, map[string]any{
		"provider": request.Provider, "model": request.Model, "effort": request.Effort,
		"step_id": request.ClientRequestID, "instructions": request.Instructions,
		"tools": request.Tools, "max_output_tokens": request.MaxOutputTokens, "input_manifest": manifest,
	})
	return ctx, func(output any, err error) {
		if failure, ok := errors.AsType[*provider.Failure](err); ok {
			p.observer.Action(ctx, "model.failure", map[string]any{
				"code": failure.Code, "http_status": failure.Status, "unknown_usage": failure.UnknownUsage,
				"retry_attempts": failure.RetryAttempts, "retry_after_ms": failure.RetryAfter.Milliseconds(),
			})
		}
		finish(output, err)
	}
}

type measuredAuth struct {
	auth     *identity.Authenticator
	observer *telemetry.Telemetry
}

func (a measuredAuth) Authenticate(ctx context.Context, r *http.Request) (domain.Principal, error) {
	return a.measure(ctx, r, a.auth.Authenticate)
}

func (a measuredAuth) AuthenticateOperator(ctx context.Context, r *http.Request) (domain.Principal, error) {
	return a.measure(ctx, r, a.auth.AuthenticateOperator)
}

func (a measuredAuth) measure(
	ctx context.Context,
	r *http.Request,
	authenticate func(context.Context, *http.Request) (domain.Principal, error),
) (domain.Principal, error) {
	ctx, finishTrace := a.observer.Start(ctx, "authorization", telemetry.RunID(ctx))
	started := time.Now()
	var err error
	defer func() { finishTrace(err) }()
	defer func() {
		a.observer.Operation(ctx, "authorization", operationOutcome(err), telemetry.RunID(ctx), started)
	}()
	principal, err := authenticate(ctx, r)
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
