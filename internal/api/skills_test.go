package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/api"
	"github.com/complynx/loom/internal/domain"
)

func TestSkillsHTTPRequiresSeparateWriteAuthority(t *testing.T) {
	t.Parallel()
	handler := api.New(nil, authFunc(allowed), api.Options{})
	for _, method := range []string{"PUT", "DELETE"} {
		response := request(handler, method, "/v1/skills/inspect-document", "{}", "application/json", "")
		require.Equal(t, http.StatusForbidden, response.Code)
	}
	handler = api.New(
		nil,
		authFunc(func(context.Context, *http.Request) (domain.Principal, error) { return domain.Principal{}, nil }),
		api.Options{},
	)
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		response := request(handler, method, "/v1/skills/inspect-document", "{}", "application/json", "")
		require.Equal(t, http.StatusUnauthorized, response.Code)
	}
}

func TestSkillsOpenAPIIncludesUsecaseAndRequirements(t *testing.T) {
	t.Parallel()
	handler := api.New(nil, authFunc(allowed), api.Options{})
	response := request(handler, "GET", "/openapi.json", "", "", "")
	require.Equal(t, http.StatusOK, response.Code)
	text := response.Body.String()
	require.Contains(t, text, `"/v1/skills/{name}"`)
	require.Contains(t, text, `"usecase"`)
	require.Contains(t, text, `"require"`)
	require.Contains(t, text, `"skills.write"`)
}
