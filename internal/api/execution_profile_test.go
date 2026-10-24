package api_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/api"
)

func TestExecutionProfileNegotiationAndValidation(t *testing.T) {
	t.Parallel()
	handler := api.New(nil, authFunc(allowed), api.Options{})
	response := request(handler, "GET", "/v1/capabilities", "", "", "")
	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), `"execution_profiles":["native_files"]`)
	response = request(
		handler,
		"POST",
		"/v1/runs",
		`{"execution_profile":"all","input":{"text":"read"}}`,
		"application/json",
		"profile",
	)
	require.Equal(t, http.StatusUnprocessableEntity, response.Code)
	require.Contains(t, response.Body.String(), "invalid_execution_profile")
}
