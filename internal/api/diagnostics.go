package api

import (
	"context"
	"net/http"

	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/complynx/loom/internal/domain"
)

const diagnosticsPermission = "diagnostics.read"
const diagnosticsTimeout = 2 * time.Second

func (s *server) registerDiagnostics(mux *http.ServeMux) {
	s.route(mux, "GET /v1/admin/diagnostics", diagnosticsPermission, s.diagnosticsOverview)
	s.route(mux, "GET /v1/admin/diagnostics/health", diagnosticsPermission, s.diagnosticsHealth)
	s.route(mux, "GET /v1/admin/diagnostics/runs", diagnosticsPermission, s.diagnosticsRuns)
	s.route(mux, "GET /v1/admin/diagnostics/runs/{id}", diagnosticsPermission, s.diagnosticsRun)
	s.registerOperatorPanel(mux)
}

func (s *server) diagnosticsOverview(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	ctx, cancel := context.WithTimeout(r.Context(), diagnosticsTimeout)
	defer cancel()
	overview, err := s.store.DiagnosticsOverview(ctx, p)
	if err != nil {
		return err
	}
	result := domain.DiagnosticsSnapshot{Overview: overview}
	if s.options.DiagnosticsHealth != nil {
		result.Runtime = s.options.DiagnosticsHealth()
	}
	write(w, http.StatusOK, result)
	return nil
}

func (s *server) diagnosticsRuns(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	input := domain.DiagnosticsRunsRequest{State: r.URL.Query().Get("state"), AfterID: r.URL.Query().Get("after_id")}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil {
			return domain.NewError(http.StatusUnprocessableEntity, "invalid_diagnostics_query")
		}
		input.Limit = limit
	}
	ctx, cancel := context.WithTimeout(r.Context(), diagnosticsTimeout)
	defer cancel()
	page, err := s.store.DiagnosticsRuns(ctx, p, input)
	if err != nil {
		return err
	}
	write(w, http.StatusOK, page)
	return nil
}

func (s *server) diagnosticsRun(w http.ResponseWriter, r *http.Request, p domain.Principal) error {
	ctx, cancel := context.WithTimeout(r.Context(), diagnosticsTimeout)
	defer cancel()
	detail, err := s.store.DiagnosticsRun(ctx, p, r.PathValue("id"))
	if err != nil {
		return err
	}
	write(w, http.StatusOK, detail)
	return nil
}

func (s *server) diagnosticsHealth(w http.ResponseWriter, _ *http.Request, _ domain.Principal) error {
	if s.options.DiagnosticsHealth == nil {
		return domain.NewError(http.StatusServiceUnavailable, "diagnostics_unavailable")
	}
	write(w, http.StatusOK, s.options.DiagnosticsHealth())
	return nil
}

func diagnosticsQueryParameters() []*huma.Param {
	const maxPageSize = 100
	const defaultPageSize = 50
	return []*huma.Param{
		{
			Name:        "state",
			In:          queryLocation,
			Description: "Empty selects nonterminal runs; all selects every state.",
			Schema:      &huma.Schema{Type: schemaString},
		},
		{
			Name:        "after_id",
			In:          queryLocation,
			Description: "UUID cursor returned as next_cursor.",
			Schema:      &huma.Schema{Type: schemaString, Format: "uuid"},
		},
		{
			Name: "limit",
			In:   queryLocation,
			Schema: &huma.Schema{
				Type:    schemaInteger,
				Minimum: new(float64(1)),
				Maximum: new(float64(maxPageSize)),
				Default: defaultPageSize,
			},
		},
	}
}
