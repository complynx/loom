package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthcheckReadiness(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable, http.StatusFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/readyz" || r.Method != http.MethodGet {
					t.Errorf("unexpected readiness request: %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Location", "/readyz")
				w.WriteHeader(status)
			}))
			defer server.Close()
			err := run([]string{"healthcheck", "--address", strings.TrimPrefix(server.URL, "http://")})
			if (err == nil) != (status == http.StatusOK) {
				t.Fatalf("status %d: %v", status, err)
			}
		})
	}
}

func TestHealthcheckRejectsExternalAddress(t *testing.T) {
	t.Parallel()
	if err := run([]string{"healthcheck", "--address", "192.0.2.1:8080"}); err == nil {
		t.Fatal("external readiness target accepted")
	}
}
