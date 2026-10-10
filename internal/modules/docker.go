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

// DockerSource uses only GET list and inspect on the operator's restricted proxy.
type DockerSource struct {
	BaseURL, UnixSocket, Network string
	Client                       *http.Client
}

func (s DockerSource) Discover(ctx context.Context) ([]Endpoint, error) {
	if s.Network == "" {
		return nil, errors.New("trusted Docker network required")
	}
	client := s.Client
	base := s.BaseURL
	if client == nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.Proxy = nil
		tr.DisableKeepAlives = true
		if s.UnixSocket != "" {
			tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", s.UnixSocket)
			}
			base = "http://docker"
		}
		client = &http.Client{Transport: tr, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("docker redirect forbidden") }}
	}
	if base == "" {
		return nil, errors.New("docker proxy address required")
	}
	var containers []struct {
		ID     string            `json:"Id"`
		Labels map[string]string `json:"Labels"`
	}
	if err := dockerGet(ctx, client, base+"/containers/json?filters="+url.QueryEscape(`{"status":["running"],"label":["clx.harness.service"]}`), &containers); err != nil {
		return nil, err
	}
	endpoints := []Endpoint{}
	for _, container := range containers {
		raw := container.Labels[ServiceLabel]
		if raw == "" {
			continue
		}
		uri, err := validateURI(raw)
		if err != nil {
			continue
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
		if err := dockerGet(ctx, client, base+"/containers/"+url.PathEscape(container.ID)+"/json", &inspection); err != nil {
			return nil, err
		}
		network, ok := inspection.NetworkSettings.Networks[s.Network]
		if !ok || !inspection.State.Running || net.ParseIP(network.IPAddress) == nil {
			continue
		}
		names := append(append([]string{strings.TrimPrefix(inspection.Name, "/")}, network.Aliases...), network.DNSNames...)
		trusted := false
		for _, name := range names {
			if strings.EqualFold(name, uri.Hostname()) {
				trusted = true
				break
			}
		}
		if !trusted {
			continue
		}
		endpoints = append(endpoints, Endpoint{ContainerID: container.ID, URI: raw, Address: network.IPAddress})
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
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20+1))
	if err != nil {
		return err
	}
	if len(raw) > 4<<20 {
		return errors.New("docker metadata too large")
	}
	return json.Unmarshal(raw, out)
}
