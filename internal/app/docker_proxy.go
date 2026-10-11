package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/complynx/clx-scripter/internal/modules"
)

const (
	containerListPath    = "/containers/json"
	dockerRequestTimeout = 3 * time.Second
	dockerResponseLimit  = 4 << 20
)

func isContainerInspectPath(path string) bool {
	const prefix = "/containers/"
	const suffix = "/json"
	const idLength = 64
	id, ok := strings.CutPrefix(path, prefix)
	if !ok {
		return false
	}
	id, ok = strings.CutSuffix(id, suffix)
	if !ok || len(id) != idLength {
		return false
	}
	for _, char := range id {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func dockerTarget(r *http.Request) (*url.URL, bool) {
	if r.Method != http.MethodGet || r.URL.EscapedPath() != r.URL.Path ||
		(r.URL.Path != containerListPath && !isContainerInspectPath(r.URL.Path)) {
		return nil, false
	}
	target := &url.URL{Scheme: "http", Host: "docker", Path: r.URL.Path}
	if r.URL.Path != containerListPath {
		return target, r.URL.RawQuery == ""
	}
	query := r.URL.Query()
	if len(query) > 1 || len(query["filters"]) > 1 {
		return nil, false
	}
	for key := range query {
		if key != "filters" {
			return nil, false
		}
	}
	target.RawQuery = url.Values{"filters": []string{`{"status":["running"],"label":["clx.harness.service"]}`}}.Encode()
	return target, true
}

// DockerProxyHandler exposes only the deployment metadata needed for discovery.
// A read-only socket mount does not restrict Docker API privileges; this allowlist
// and a separate credential-free process are the actual access boundary.
func DockerProxyHandler(client *http.Client) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		target, allowed := dockerTarget(r)
		if !allowed {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), dockerRequestTimeout)
		defer cancel()
		// #nosec G704 -- dockerTarget fixes scheme and host and accepts only allowlisted metadata paths.
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
		if err != nil {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		// #nosec G704 -- the fixed Docker destination uses an operator-supplied transport, never request authority.
		response, err := client.Do(request)
		if err != nil {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != http.StatusOK {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, dockerResponseLimit+1))
		if err != nil || len(raw) > dockerResponseLimit {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == containerListPath {
			writeContainerList(w, raw)
			return
		}
		writeContainerInspection(w, raw)
	})
}
func ServeDockerProxy(ctx context.Context, listen, socket string) error {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return errors.New("default HTTP transport unavailable")
	}
	transport = transport.Clone()
	transport.Proxy = nil
	transport.DialContext = func(c context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(c, "unix", socket)
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     transport,
		Timeout:       dockerRequestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	server := &http.Server{
		Addr:              listen,
		Handler:           DockerProxyHandler(client),
		ReadHeaderTimeout: dockerRequestTimeout,
		WriteTimeout:      dockerWriteTimeout,
		IdleTimeout:       dockerIdleTimeout,
		MaxHeaderBytes:    dockerMaxHeaderBytes,
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", listen)
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

func writeContainerList(w http.ResponseWriter, raw []byte) {
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
			sanitized = append(
				sanitized,
				map[string]any{"Id": row.ID, "Labels": map[string]string{modules.ServiceLabel: label}},
			)
		}
	}
	_ = json.NewEncoder(w).Encode(sanitized)
}
func writeContainerInspection(w http.ResponseWriter, raw []byte) {
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
			} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if json.Unmarshal(raw, &inspection) != nil || inspection.Config.Labels[modules.ServiceLabel] == "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	_ = json.NewEncoder(w).
		Encode(map[string]any{"Id": inspection.ID, "Name": inspection.Name, "State": inspection.State, "Config": map[string]any{"Labels": map[string]string{modules.ServiceLabel: inspection.Config.Labels[modules.ServiceLabel]}}, "NetworkSettings": inspection.NetworkSettings})
}
