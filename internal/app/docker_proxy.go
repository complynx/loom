package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/complynx/clx-scripter/internal/modules"
)

var containerInspectPath = regexp.MustCompile(`^/containers/[0-9a-f]{64}/json$`)

// DockerProxyHandler exposes only the deployment metadata needed for discovery.
// A read-only socket mount does not restrict Docker API privileges; this allowlist
// and a separate credential-free process are the actual access boundary.
func DockerProxyHandler(client *http.Client) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet || r.URL.EscapedPath() != r.URL.Path || (r.URL.Path != "/containers/json" && !containerInspectPath.MatchString(r.URL.Path)) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		target := &url.URL{Scheme: "http", Host: "docker", Path: r.URL.Path}
		if r.URL.Path == "/containers/json" {
			query := r.URL.Query()
			if len(query) > 1 || len(query["filters"]) > 1 {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			for key := range query {
				if key != "filters" {
					http.Error(w, "forbidden", http.StatusForbidden)
					return
				}
			}
			target.RawQuery = url.Values{"filters": []string{`{"status":["running"],"label":["clx.harness.service"]}`}}.Encode()
		} else if r.URL.RawQuery != "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
		if err != nil {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		response, err := client.Do(request)
		if err != nil {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != 200 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
		if err != nil || len(raw) > 4<<20 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/containers/json" {
			var rows []struct {
				ID     string            `json:"Id"`
				Labels map[string]string `json:"Labels"`
			}
			if json.Unmarshal(raw, &rows) != nil {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			sanitized := []map[string]any{}
			for _, row := range rows {
				label := row.Labels[modules.ServiceLabel]
				if label != "" {
					sanitized = append(sanitized, map[string]any{"Id": row.ID, "Labels": map[string]string{modules.ServiceLabel: label}})
				}
			}
			_ = json.NewEncoder(w).Encode(sanitized)
			return
		}
		var inspection struct {
			ID    string `json:"Id"`
			Name  string `json:"Name"`
			State struct {
				Running bool `json:"Running"`
			} `json:"State"`
			Config struct {
				Labels map[string]string `json:"Labels"`
			} `json:"Config"`
			NetworkSettings struct {
				Networks map[string]struct {
					IPAddress string   `json:"IPAddress"`
					Aliases   []string `json:"Aliases"`
					DNSNames  []string `json:"DNSNames"`
				}
			} `json:"NetworkSettings"`
		}
		if json.Unmarshal(raw, &inspection) != nil || inspection.Config.Labels[modules.ServiceLabel] == "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": inspection.ID, "Name": inspection.Name, "State": inspection.State, "Config": map[string]any{"Labels": map[string]string{modules.ServiceLabel: inspection.Config.Labels[modules.ServiceLabel]}}, "NetworkSettings": inspection.NetworkSettings})
	})
}
func ServeDockerProxy(ctx context.Context, listen, socket string) error {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(c context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(c, "unix", socket)
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	server := &http.Server{Addr: listen, Handler: DockerProxyHandler(client), ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 4 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 4096}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return errors.New("docker discovery proxy listener unavailable")
	}
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		if server.Shutdown(closeCtx) != nil {
			_ = server.Close()
		}
		return nil
	case err = <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.New("docker discovery proxy stopped")
	}
}
