package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/complynx/clx-scripter/internal/domain"
	"github.com/complynx/clx-scripter/internal/modules"
)

const diagnosticsModule = "diagnostics"
const diagnosticsPermission = "diagnostics.read"

// Operators retain builtin diagnostics when the remote discovery catalog is unavailable.
func (t runtimeTools) diagnosticReadCatalog(
	ctx context.Context,
	p modules.Principal,
	fresh domain.Principal,
) ([]modules.Tool, error) {
	reads, err := t.registry.List(ctx, p)
	if err != nil && fresh.Has(diagnosticsPermission) && ctx.Err() == nil {
		return nil, nil
	}
	return reads, err
}

func (t runtimeTools) diagnostics(
	ctx context.Context,
	p domain.Principal,
	method string,
	raw json.RawMessage,
) (json.RawMessage, error) {
	started := time.Now()
	result, err := t.readDiagnostics(ctx, p, method, raw)
	if t.observer != nil {
		t.observer.Operation(ctx, diagnosticsModule, operationOutcome(err), "", started)
	}
	return result, err
}

func (t runtimeTools) readDiagnostics(
	ctx context.Context,
	p domain.Principal,
	method string,
	raw json.RawMessage,
) (json.RawMessage, error) {
	var err error
	if !p.Has(diagnosticsPermission) {
		return nil, domain.Forbidden()
	}
	work, cancel := context.WithTimeout(ctx, telemetryTimeout)
	defer cancel()
	switch method {
	case "health":
		var input struct{}
		if err = decodeDiagnosticsInput(raw, &input); err != nil {
			return nil, err
		}
		if t.observer == nil {
			return nil, domain.NewError(http.StatusServiceUnavailable, "diagnostics_unavailable")
		}
		return json.Marshal(t.observer.DiagnosticsHealth())
	case "overview":
		var input struct{}
		if err = decodeDiagnosticsInput(raw, &input); err != nil {
			return nil, err
		}
		overview, readErr := t.repository.DiagnosticsOverview(work, p)
		if readErr != nil {
			return nil, readErr
		}
		snapshot := domain.DiagnosticsSnapshot{Overview: overview}
		if t.observer != nil {
			snapshot.Runtime = t.observer.DiagnosticsHealth()
		}
		return json.Marshal(snapshot)
	case "runs":
		var input domain.DiagnosticsRunsRequest
		if err = decodeDiagnosticsInput(raw, &input); err != nil {
			return nil, err
		}
		page, readErr := t.repository.DiagnosticsRuns(work, p, input)
		if readErr != nil {
			return nil, readErr
		}
		return json.Marshal(page)
	case "run":
		var input struct {
			ID string `json:"run_id"`
		}
		if err = decodeDiagnosticsInput(raw, &input); err != nil {
			return nil, err
		}
		detail, readErr := t.repository.DiagnosticsRun(work, p, input.ID)
		if readErr != nil {
			return nil, readErr
		}
		return json.Marshal(detail)
	default:
		return nil, domain.NotFound()
	}
}

func decodeDiagnosticsInput(raw json.RawMessage, target any) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return domain.Conflict("invalid_diagnostics_input")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return domain.Conflict("invalid_diagnostics_input")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return domain.Conflict("invalid_diagnostics_input")
	}
	return nil
}
