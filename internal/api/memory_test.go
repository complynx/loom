package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/complynx/clx-scripter/internal/api"
	"github.com/complynx/clx-scripter/internal/domain"
	"github.com/complynx/clx-scripter/internal/store"
)

func TestPersonalMemoryRequiresAuthentication(t *testing.T) {
	t.Parallel()
	handler := api.New(
		nil,
		authFunc(func(context.Context, *http.Request) (domain.Principal, error) { return domain.Principal{}, nil }),
		api.Options{},
	)
	for _, operation := range []struct{ method, path string }{
		{"GET", "/v1/users/me/memory"},
		{"PUT", "/v1/users/me/memory/micro"},
		{"PUT", "/v1/users/me/memory/index"},
		{"DELETE", "/v1/users/me/memory"},
		{"POST", "/v1/users/me/memory/search"},
		{"GET", "/v1/users/me/memory/entries/private"},
		{"PUT", "/v1/users/me/memory/entries/private"},
		{"DELETE", "/v1/users/me/memory/entries/private"},
	} {
		response := request(handler, operation.method, operation.path, "{}", "application/json", "")
		require.Equal(t, http.StatusUnauthorized, response.Code)
		require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	}
}

func TestPersonalMemoryStrictRequests(t *testing.T) {
	t.Parallel()
	handler := api.New(nil, authFunc(allowed), api.Options{})
	for _, operation := range []struct{ method, path, body string }{
		{"PUT", "/v1/users/me/memory/micro", `{"text":"a","version":0,"subject":"other"}`},
		{"PUT", "/v1/users/me/memory/index", `{"text":"a","version":0,"org_id":"other"}`},
		{"DELETE", "/v1/users/me/memory", `{"revision":0,"user_id":"other"}`},
		{"POST", "/v1/users/me/memory/search", `{"query":"a","user_id":"other"}`},
		{"PUT", "/v1/users/me/memory/entries/a", `{"kind":"note","version":0,"issuer":"other"}`},
		{"DELETE", "/v1/users/me/memory/entries/a", `{"version":0,"subject":"other"}`},
	} {
		response := request(handler, operation.method, operation.path, operation.body, "application/json", "")
		require.Equal(t, http.StatusUnprocessableEntity, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), "invalid_request")
	}
	response := request(handler, "GET", "/v1/users/me/memory/entries/a?offset=invalid", "", "", "")
	require.Equal(t, http.StatusUnprocessableEntity, response.Code)
	require.Contains(t, response.Body.String(), "invalid_memory_query")
}

func TestPersonalMemoryOpenAPIContract(t *testing.T) {
	t.Parallel()
	response := request(api.New(nil, authFunc(allowed), api.Options{}), "GET", "/openapi.json", "", "", "")
	require.Equal(t, http.StatusOK, response.Code)
	var document struct {
		Paths map[string]map[string]map[string]json.RawMessage `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &document))
	read := document.Paths["/v1/users/me/memory"]["get"]
	require.JSONEq(t, `""`, string(read["x-required-permission"]))
	write := document.Paths["/v1/users/me/memory/micro"]["put"]
	require.JSONEq(t, `""`, string(write["x-required-permission"]))
	require.Contains(t, string(write["requestBody"]), `"version"`)
	require.Contains(t, string(read["responses"]), `"micro"`)
	entry := document.Paths["/v1/users/me/memory/entries/{key}"]["get"]
	require.Contains(t, string(entry["parameters"]), `"key"`)
	require.Contains(t, string(entry["parameters"]), `"offset"`)
	require.Contains(t, string(entry["parameters"]), `"limit"`)
}

func TestPersonalMemoryHTTPPersistence(t *testing.T) {
	t.Parallel()
	dsn := os.Getenv("CLX_TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("CLX_TEST_DATABASE_DSN is required for PostgreSQL behavior tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	schema := "memory_api_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		require.NoError(t, cleanupErr)
		admin.Close()
	})
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	require.NoError(t, store.Migrate(ctx, parsed.String()))
	pool, err := pgxpool.New(ctx, parsed.String())
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	repository := store.New(pool, nil)
	require.NoError(t, repository.Bootstrap(ctx, domain.Settings{
		Models: []domain.Model{
			{ID: "test", ProviderModel: "test", Efforts: []string{"low"}, MaxEffort: "low", MaxOutputTokens: 100},
		},
		DefaultModelID:         "test",
		DefaultEffort:          "low",
		InitialCredits:         10000,
		MaxRunMicrocredits:     1000,
		ActiveRunLimit:         8,
		HistoryRecentLimit:     50,
		MaxSteps:               10,
		QuestionTimeoutSeconds: 60,
	}))
	handler := api.New(repository, authFunc(allowed), api.Options{})
	response := request(handler, "GET", "/v1/users/me/memory", "", "", "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	response = request(
		handler,
		"PUT",
		"/v1/users/me/memory/micro",
		`{"text":"Prefers Russian","version":0}`,
		"application/json",
		"",
	)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var document domain.MemoryDocument
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &document))
	require.Equal(t, int64(1), document.Version)
	response = request(
		handler,
		"PUT",
		"/v1/users/me/memory/micro",
		`{"text":"Stale overwrite","version":0}`,
		"application/json",
		"",
	)
	require.Equal(t, http.StatusConflict, response.Code)
	response = request(
		handler,
		"PUT",
		"/v1/users/me/memory/entries/language",
		`{"kind":"note","title":"Language","summary":"Russian","body":"Evidence: user preference","version":0}`,
		"application/json",
		"",
	)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	response = request(handler, "POST", "/v1/users/me/memory/search", `{"query":"Russian"}`, "application/json", "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), `"key":"language"`)
	response = request(handler, "GET", "/v1/users/me/memory/entries/language?limit=8", "", "", "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), `"partial":true`)
	foreign := authFunc(func(ctx context.Context, r *http.Request) (domain.Principal, error) {
		p, authErr := allowed(ctx, r)
		p.Subject = "bob"
		p.Actor = "bob"
		return p, authErr
	})
	response = request(
		api.New(store.New(pool, nil), foreign, api.Options{}),
		"GET",
		"/v1/users/me/memory/entries/language",
		"",
		"",
		"",
	)
	require.Equal(t, http.StatusNotFound, response.Code)
	response = request(handler, "GET", "/v1/users/me/memory", "", "", "")
	var state domain.MemoryState
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &state))
	payload, err := json.Marshal(map[string]int64{"revision": state.Revision})
	require.NoError(t, err)
	response = request(handler, "DELETE", "/v1/users/me/memory", string(payload), "application/json", "")
	require.Equal(t, http.StatusNoContent, response.Code, response.Body.String())
	response = request(handler, "GET", "/v1/users/me/memory/entries/language", "", "", "")
	require.Equal(t, http.StatusNotFound, response.Code)
}
