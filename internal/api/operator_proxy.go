package api

import (
	"context"
	"net/http"
	"strings"
)

// operatorProxy exposes read-only diagnostics under the browser's SSO router.
// It forwards the access token unchanged to the normal API authorization path.
// It never accepts identity headers or grants permissions based on proxy access.
func (s *server) operatorProxy(api http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Browser diagnostics are read-only", http.StatusMethodNotAllowed)
			return
		}
		origin := r.Header.Get("Origin")
		if (origin != "" && origin != s.options.OperatorProxyOrigin) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "Browser origin denied", http.StatusForbidden)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/admin/api")
		if !operatorDiagnosticPath(path) {
			http.NotFound(w, r)
			return
		}
		forward := r.Clone(context.WithValue(r.Context(), operatorRequestKey{}, true))
		forward.URL.Path = "/v1/admin" + path
		forward.URL.RawPath = ""
		api.ServeHTTP(w, forward)
	})
}

func operatorDiagnosticPath(path string) bool {
	switch path {
	case "/diagnostics", "/diagnostics/health", "/diagnostics/runs":
		return true
	default:
		id, found := strings.CutPrefix(path, "/diagnostics/runs/")
		return found && id != "" && !strings.ContainsAny(id, "/.")
	}
}
