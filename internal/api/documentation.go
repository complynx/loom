package api

import (
	"net/http"
	"reflect"
	"strings"

	"github.com/complynx/clx-scripter/internal/domain"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

// Documentation uses the same DTOs as strict HTTP decoding. SSE and upload
// handlers remain standard HTTP handlers so deadlines and body limits stay explicit.
func documentation(mux *http.ServeMux) huma.API {
	cfg := huma.DefaultConfig("CLX Scripter", "1.0.0")
	cfg.DocsPath = ""
	cfg.SchemasPath = ""
	cfg.OpenAPIPath = "/openapi"
	api := humago.New(mux, cfg)
	api.OpenAPI().Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"bearer": {Type: "http", Scheme: "bearer"},
	}
	return api
}
func documentOperation(api huma.API, pattern, permission string) {
	parts := strings.SplitN(pattern, " ", 2)
	method, path := parts[0], parts[1]
	op := &huma.Operation{Method: method, Path: path, OperationID: strings.ToLower(method) + "-" + strings.NewReplacer("/", "-", "{", "", "}", "").Replace(path), Security: []map[string][]string{{"bearer": {}}}, Responses: map[string]*huma.Response{"default": {Description: "Structured error; details in docs/api.md"}}, Extensions: map[string]any{"x-required-permission": permission}}
	if strings.Contains(path, "{id}") {
		op.Parameters = append(op.Parameters, &huma.Param{Name: "id", In: "path", Required: true, Schema: &huma.Schema{Type: "string"}})
	}
	if method == "POST" && (path == "/v1/runs" || strings.HasPrefix(path, "/v1/runs/") && !strings.HasSuffix(path, "/events") || strings.HasSuffix(path, "/adjustments") || strings.HasSuffix(path, "/reconcile")) {
		op.Parameters = append(op.Parameters, &huma.Param{Name: "Idempotency-Key", In: "header", Required: true, Schema: &huma.Schema{Type: "string", MaxLength: pointer(128)}})
	}
	if strings.Contains(path, "{step}") {
		op.Parameters = append(op.Parameters, &huma.Param{Name: "step", In: "path", Required: true, Schema: &huma.Schema{Type: "string"}})
	}
	var request, response any
	status := "200"
	if method == "DELETE" {
		status = "204"
	}
	switch {
	case strings.HasSuffix(path, "/reconcile"):
		request = domain.ReconcileStepRequest{}
	case path == "/v1/runs" && method == "POST":
		request = domain.CreateRun{}
		response = domain.Run{}
		status = "202"
	case strings.HasPrefix(path, "/v1/runs/") && strings.HasSuffix(path, "/input"):
		request = inputRequest{}
		response = runMutationReceipt{}
	case strings.HasPrefix(path, "/v1/runs/") && method == "POST":
		response = runMutationReceipt{}
	case strings.HasPrefix(path, "/v1/runs/") && !strings.HasSuffix(path, "/events"):
		response = domain.Run{}
	case strings.HasSuffix(path, "/history/search"):
		request = domain.SearchRequest{}
		response = domain.SearchResult{}
	case strings.HasPrefix(path, "/v1/chats/") && strings.HasSuffix(path, "/events"):
		request = domain.AppendEvent{}
		response = domain.ChatEvent{}
		status = "201"
	case path == "/v1/chats" || path == "/v1/chats/default":
		response = domain.Chat{}
		if method == "POST" {
			request = struct{}{}
			status = "201"
		}
	case strings.HasSuffix(path, "/preferences"):
		response = domain.Preferences{}
		if method == "PATCH" {
			request = domain.Preferences{}
		}
	case path == "/v1/admin/settings":
		response = domain.Settings{}
		if method == "PATCH" {
			request = domain.Settings{}
		}
	case strings.HasSuffix(path, "/policy"):
		response = domain.UserPolicy{}
		if method == "PATCH" {
			request = domain.UserPolicy{}
		}
	case path == "/v1/users/me/credits":
		response = domain.Balance{}
	case strings.HasSuffix(path, "/adjustments") || strings.HasSuffix(path, "/reconcile"):
		request = struct {
			Amount int64
			Reason string
		}{}
		response = domain.Balance{}
	case strings.HasPrefix(path, "/v1/artifacts") && !strings.HasSuffix(path, "/content"):
		response = domain.Artifact{}
		if method == "POST" {
			status = "201"
		}
	}
	schema := func(v any) *huma.Schema {
		return huma.SchemaFromType(api.OpenAPI().Components.Schemas, reflect.TypeOf(v))
	}
	if request != nil {
		op.RequestBody = &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{"application/json": {Schema: schema(request)}}}
	}
	op.Responses[status] = &huma.Response{Description: "Durable result"}
	if response != nil {
		op.Responses[status].Content = map[string]*huma.MediaType{"application/json": {Schema: schema(response)}}
	}
	if path == "/v1/artifacts" && method == "POST" {
		op.RequestBody = &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{"application/octet-stream": {Schema: &huma.Schema{Type: "string", Format: "binary"}}}}
	}
	if strings.HasPrefix(path, "/v1/runs/") && strings.HasSuffix(path, "/events") {
		op.Responses["200"].Content = map[string]*huma.MediaType{"text/event-stream": {Schema: &huma.Schema{Type: "string"}}}
	}
	api.OpenAPI().AddOperation(op)
}
func pointer(v int) *int { return &v }
