package modules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const ServiceLabel = "clx.harness.service"

const (
	dockerTimeout          = 10 * time.Second
	maxDockerMetadataBytes = 4 << 20
)

// DockerSource uses only GET list and inspect on the operator's restricted proxy.
type DockerSource struct {
	BaseURL, UnixSocket, Network string
	Client                       *http.Client
}

func (s DockerSource) Discover(ctx context.Context) ([]Endpoint, error) {
	if s.Network == "" {
		return nil, errors.New("trusted Docker network required")
	}
	client, base, err := s.client()
	if err != nil {
		return nil, err
	}
	var containers []struct {
		ID     string            `json:"Id"`
		Labels map[string]string `json:"Labels"`
	}
	if listErr := dockerGet(
		ctx,
		client,
		base+"/containers/json?filters="+url.QueryEscape(`{"status":["running"],"label":["clx.harness.service"]}`),
		&containers,
	); listErr != nil {
		return nil, listErr
	}
	endpoints := []Endpoint{}
	for _, container := range containers {
		endpoint, inspectErr := s.inspectEndpoint(ctx, client, base, container.ID, container.Labels[ServiceLabel])
		if inspectErr != nil {
			return nil, inspectErr
		}
		if endpoint.ContainerID != "" {
			endpoints = append(endpoints, endpoint)
		}
	}
	return endpoints, nil
}
func dockerGet(ctx context.Context, client *http.Client, address string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("docker HTTP status %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxDockerMetadataBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maxDockerMetadataBytes {
		return errors.New("docker metadata too large")
	}
	return json.Unmarshal(raw, out)
}

func (s DockerSource) client() (*http.Client, string, error) {
	client := s.Client
	base := s.BaseURL
	if client == nil {
		defaultTransport, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return nil, "", errors.New("unsupported default HTTP transport")
		}
		tr := defaultTransport.Clone()
		tr.Proxy = nil
		tr.DisableKeepAlives = true
		if s.UnixSocket != "" {
			tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", s.UnixSocket)
			}
			base = "http://docker"
		}
		client = &http.Client{
			Transport:     tr,
			Timeout:       dockerTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("docker redirect forbidden") },
		}
	}
	if base == "" {
		return nil, "", errors.New("docker proxy address required")
	}
	return client, base, nil
}

func (s DockerSource) inspectEndpoint(
	ctx context.Context,
	client *http.Client,
	base, containerID, raw string,
) (Endpoint, error) {
	if raw == "" {
		return Endpoint{}, nil
	}
	uri, err := validateURI(raw)
	if err != nil {
		return Endpoint{}, nil //nolint:nilerr // Ignore malformed labels; only validated endpoints enter discovery.
	}
	var inspection struct {
		Name  string `json:"Name"`
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
		NetworkSettings struct {
			Networks map[string]struct {
				IPAddress string   `json:"IPAddress"`
				Aliases   []string `json:"Aliases"`
				DNSNames  []string `json:"DNSNames"`
			} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if inspectErr := dockerGet(
		ctx,
		client,
		base+"/containers/"+url.PathEscape(containerID)+"/json",
		&inspection,
	); inspectErr != nil {
		return Endpoint{}, inspectErr
	}
	network, ok := inspection.NetworkSettings.Networks[s.Network]
	if !ok || !inspection.State.Running || net.ParseIP(network.IPAddress) == nil {
		return Endpoint{}, nil
	}
	names := append(
		append([]string{strings.TrimPrefix(inspection.Name, "/")}, network.Aliases...),
		network.DNSNames...)
	trusted := false
	for _, name := range names {
		if strings.EqualFold(name, uri.Hostname()) {
			trusted = true
			break
		}
	}
	if !trusted {
		return Endpoint{}, nil
	}
	return Endpoint{ContainerID: containerID, URI: raw, Address: network.IPAddress}, nil
}
