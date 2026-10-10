package app

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type proxyTransport func(*http.Request) (*http.Response, error)

func (f proxyTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestDockerProxyRejectsMutationAndArbitraryRoutes(t *testing.T) {
	client := &http.Client{Transport: proxyTransport(func(*http.Request) (*http.Response, error) { t.Error("denied request reached Docker"); return nil, nil })}
	handler := DockerProxyHandler(client)
	for _, input := range []struct{ method, path string }{{"POST", "/containers/json"}, {"DELETE", "/containers/" + strings.Repeat("a", 64) + "/json"}, {"GET", "/info"}, {"GET", "/containers/%2e%2e/json"}, {"GET", "/containers/json?all=1"}, {"GET", "/containers/" + strings.Repeat("a", 64) + "/json?size=true"}} {
		t.Run(input.method+input.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(input.method, input.path, nil))
			require.Equal(t, http.StatusForbidden, response.Code)
		})
	}
}
func TestDockerProxyLimitsListAndRedactsInspectSecrets(t *testing.T) {
	id := strings.Repeat("a", 64)
	client := &http.Client{Transport: proxyTransport(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "docker", r.URL.Host)
		require.Empty(t, r.Header.Get("Authorization"))
		body := ""
		if r.URL.Path == "/containers/json" {
			var filter map[string][]string
			require.NoError(t, json.Unmarshal([]byte(r.URL.Query().Get("filters")), &filter))
			require.Equal(t, []string{"running"}, filter["status"])
			require.Equal(t, []string{"clx.harness.service"}, filter["label"])
			body = `[{"Id":"` + id + `","Labels":{"clx.harness.service":"http://orders:8080/manifest","private.key":"list-secret"},"Command":"secretcommand"}]`
		} else {
			body = `{"Id":"` + id + `","Name":"/orders","State":{"Running":true},"Config":{"Labels":{"clx.harness.service":"http://orders:8080/manifest","private.key":"label-secret"},"Env":["API_KEY=environment-secret"]},"Mounts":[{"Source":"host-secret"}],"NetworkSettings":{"Networks":{"agent-net":{"IPAddress":"172.18.0.2","Aliases":["orders"]}}}}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	handler := DockerProxyHandler(client)
	for _, path := range []string{"/containers/json?filters=attacker-input", "/containers/" + id + "/json"} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer inbound-secret")
		handler.ServeHTTP(response, request)
		require.Equal(t, 200, response.Code)
		require.Contains(t, response.Body.String(), "orders")
		require.NotContains(t, response.Body.String(), "secret")
		require.NotContains(t, response.Body.String(), "Mounts")
		require.NotContains(t, response.Body.String(), "Env")
	}
}
