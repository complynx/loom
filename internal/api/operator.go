package api

import (
	"embed"
	"net/http"
)

//go:embed operator.html operator.js operator.css
var operatorAssets embed.FS

// The public shell contains no data. Every API call requires fresh bearer authorization.
func (s *server) registerOperatorPanel(mux *http.ServeMux) {
	s.registerOperatorLogin(mux)
	if s.options.OperatorProxyOrigin != "" {
		mux.Handle("/admin/api/", s.operatorProxy(mux))
	}
	for _, asset := range []struct{ path, file, media string }{
		{operatorPanelPath, "operator.html", "text/html; charset=utf-8"},
		{"/admin/operator.js", "operator.js", "text/javascript; charset=utf-8"},
		{"/admin/operator.css", "operator.css", "text/css; charset=utf-8"},
	} {
		mux.HandleFunc("GET "+asset.path, func(w http.ResponseWriter, _ *http.Request) {
			data, err := operatorAssets.ReadFile(asset.file)
			if err != nil {
				http.Error(w, "Panel unavailable", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", asset.media)
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().
				Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
			_, _ = w.Write(data)
		})
	}
}
