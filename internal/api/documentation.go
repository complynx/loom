package api

import (
	"net/http"
	"reflect"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/complynx/loom/internal/domain"
)

const (
	bearerScheme   = "bearer"
	schemaString   = "string"
	schemaInteger  = "integer"
	binaryFormat   = "binary"
	queryLocation  = "query"
	headerLocation = "header"
	runsPath       = "/v1/runs"
)

// Documentation uses the same DTOs as strict HTTP decoding. SSE and upload
// handlers remain standard HTTP handlers so deadlines and body limits stay explicit.
func documentation(mux *http.ServeMux) huma.API {
	cfg := huma.DefaultConfig("Loom", "1.0.0")
	cfg.DocsPath = ""
	cfg.SchemasPath = ""
	cfg.OpenAPIPath = "/openapi"
	api := humago.New(mux, cfg)
	api.OpenAPI().Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		bearerScheme: {Type: "http", Scheme: bearerScheme},
	}
	return api
}
func documentOperation(api huma.API, pattern, permission string) {
	method, path, _ := strings.Cut(pattern, " ")
	op := &huma.Operation{
		Method:      method,
		Path:        path,
		OperationID: strings.ToLower(method) + "-" + strings.NewReplacer("/", "-", "{", "", "}", "").Replace(path),
		Security:    []map[string][]string{{bearerScheme: {}}},
		Responses:   map[string]*huma.Response{"default": {Description: "Structured error; details in docs/api.md"}},
		Extensions:  map[string]any{"x-required-permission": permission},
	}
	if strings.Contains(path, "{id}") {
		op.Parameters = append(
			op.Parameters,
			&huma.Param{Name: "id", In: "path", Required: true, Schema: &huma.Schema{Type: schemaString}},
		)
	}
	if method == http.MethodPost &&
		(path == runsPath || strings.HasPrefix(path, "/v1/runs/") && !strings.HasSuffix(path, "/events") && !strings.HasSuffix(path, "/artifacts/handles") || strings.HasSuffix(path, "/adjustments") || strings.HasSuffix(path, "/reconcile") || strings.HasSuffix(path, "/recovery/prepare")) {
		op.Parameters = append(
			op.Parameters,
			&huma.Param{
				Name:     "Idempotency-Key",
				In:       headerLocation,
				Required: true,
				Schema:   &huma.Schema{Type: schemaString, MaxLength: new(maxIdempotencyKeyLength)},
			},
		)
	}
	if strings.Contains(path, "{step}") {
		op.Parameters = append(
			op.Parameters,
			&huma.Param{Name: "step", In: "path", Required: true, Schema: &huma.Schema{Type: schemaString}},
		)
	}
	if path == "/v1/admin/diagnostics/runs" {
		op.Parameters = append(op.Parameters, diagnosticsQueryParameters()...)
	}
	op.Parameters = append(op.Parameters, historyAndStreamParameters(method, path)...)
	request, response, status := operationTypes(method, path)
	schema := func(v any) *huma.Schema {
		return huma.SchemaFromType(api.OpenAPI().Components.Schemas, reflect.TypeOf(v))
	}
	if request != nil {
		op.RequestBody = &huma.RequestBody{
			Required: true,
			Content:  map[string]*huma.MediaType{jsonMediaType: {Schema: schema(request)}},
		}
	}
	op.Responses[status] = &huma.Response{Description: "Durable result"}
	if response != nil {
		op.Responses[status].Content = map[string]*huma.MediaType{jsonMediaType: {Schema: schema(response)}}
	}
	if path == runsPath && method == http.MethodPost {
		op.Responses["200"] = &huma.Response{
			Description: "Completed within the requested wait window",
			Content:     op.Responses[status].Content,
		}
	}
	if path == "/v1/artifacts" && method == http.MethodPost {
		op.RequestBody = artifactUploadBody()
	}
	if strings.HasPrefix(path, "/v1/runs/") && strings.HasSuffix(path, "/events") {
		op.Responses["200"].Content = map[string]*huma.MediaType{
			"text/event-stream": {Schema: &huma.Schema{Type: schemaString}},
		}
	}
	if artifactContentOperation(op) {
		artifactHeadOperation(api, op)
	}
	api.OpenAPI().AddOperation(op)
}
func operationTypes(method, path string) (any, any, string) {
	if request, response, status, matched := sessionAndReadTypes(method, path); matched {
		return request, response, status
	}
	status := "200"
	if method == http.MethodDelete {
		status = "204"
	}
	switch {
	case strings.HasSuffix(path, "/recovery/prepare"):
		return domain.RecoveryPrepareRequest{}, recoveryPreparationReceipt{}, status
	case strings.HasSuffix(path, "/reconcile"):
		return domain.ReconcileStepRequest{}, reconciliationReceipt{}, status
	case strings.HasSuffix(path, "/artifacts/handles"):
		return struct {
			ArtifactID string `json:"artifact_id"`
		}{}, domain.ArtifactHandle{}, "201"
	case strings.HasSuffix(path, "/history/read"):
		return domain.ReadHistoryRequest{}, domain.SearchResult{}, status
	case strings.HasSuffix(path, "/history/embeddings"):
		return domain.EventEmbedding{}, nil, "204"
	case path == runsPath && method == http.MethodPost:
		return domain.CreateRun{}, domain.Run{}, "202"
	case strings.HasPrefix(path, "/v1/runs/") && strings.HasSuffix(path, "/input"):
		return inputRequest{}, runInputReceipt{}, status
	case strings.HasPrefix(path, "/v1/runs/") && method == http.MethodPost:
		return nil, runMutationReceipt{}, status
	case strings.HasPrefix(path, "/v1/runs/") && !strings.HasSuffix(path, "/events"):
		return nil, domain.Run{}, status
	case strings.HasSuffix(path, "/history/search"):
		return domain.SearchRequest{}, domain.SearchResult{}, status
	case strings.HasPrefix(path, "/v1/chats/") && strings.HasSuffix(path, "/events"):
		return domain.AppendEvent{}, domain.ChatEvent{}, "201"
	case path == "/v1/chats" || path == "/v1/chats/default":
		if method == http.MethodPost {
			return struct{}{}, domain.Chat{}, "201"
		}
		return nil, domain.Chat{}, status
	case strings.HasSuffix(path, "/preferences"):
		return patchRequest(method, domain.Preferences{}), domain.Preferences{}, status
	case path == "/v1/admin/settings":
		return patchRequest(method, domain.Settings{}), domain.Settings{}, status
	case strings.HasSuffix(path, "/policy"):
		return patchRequest(method, domain.UserPolicy{}), domain.UserPolicy{}, status
	case path == "/v1/users/me/credits":
		return nil, domain.Balance{}, status
	case strings.HasSuffix(path, "/adjustments"):
		return billingAdjustmentRequest{}, domain.Balance{}, status
	case strings.HasPrefix(path, "/v1/artifacts") && !strings.HasSuffix(path, "/content"):
		if method == http.MethodPost {
			status = "201"
		}
		return nil, domain.Artifact{}, status
	}
	return nil, nil, status
}

func sessionAndReadTypes(method, path string) (any, any, string, bool) {
	if strings.HasSuffix(path, "/reactions") {
		return domain.SetReaction{}, domain.ChatEvent{}, "201", true
	}
	if path == "/v1/admin/identity/cache/clear" {
		return nil, identityCacheReceipt{}, "200", true
	}
	if strings.HasPrefix(path, "/v1/auth/sessions") {
		request, response, status := authSessionTypes(method, path)
		return request, response, status, true
	}
	if strings.HasPrefix(path, "/v1/admin/diagnostics") {
		return nil, diagnosticsResponseType(path), "200", true
	}
	if path == "/v1/models" {
		return nil, modelsResponse{}, "200", true
	}
	if method == http.MethodGet && strings.HasSuffix(path, "/history") {
		return nil, historyResponse{}, "200", true
	}
	return nil, nil, "", false
}

func authSessionTypes(method, path string) (any, any, string) {
	if path == "/v1/auth/sessions" && method == http.MethodPost {
		return struct{}{}, authSessionReceipt{}, "201"
	}
	if strings.HasSuffix(path, "/reauthorize") {
		return reauthorizeSessionRequest{}, authSessionReceipt{}, "200"
	}
	return nil, nil, "204"
}

func patchRequest(method string, value any) any {
	if method == http.MethodPatch {
		return value
	}
	return nil
}

func diagnosticsResponseType(path string) any {
	switch path {
	case "/v1/admin/diagnostics":
		return domain.DiagnosticsSnapshot{}
	case "/v1/admin/diagnostics/health":
		return domain.DiagnosticsHealth{}
	case "/v1/admin/diagnostics/runs":
		return domain.DiagnosticsRunsPage{}
	default:
		return domain.DiagnosticsRunDetail{}
	}
}
