package main

import (
	"context"
	"errors"
	"flag"
	"net"
	"net/http"
	"time"
)

const readinessProbeTimeout = 3 * time.Second

// runHealthcheck probes readiness without configuration, credentials or external tools.
func runHealthcheck(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	address := flags.String("address", "127.0.0.1:8080", "loopback API address")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	host, _, err := net.SplitHostPort(*address)
	if flags.NArg() != 0 || err != nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("healthcheck requires a loopback IP and port")
	}
	client := &http.Client{
		Timeout:       readinessProbeTimeout,
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer client.CloseIdleConnections()
	// #nosec G704 -- CLI target is restricted to literal loopback IPs above; redirects and proxies are disabled.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+*address+"/readyz", nil)
	if err != nil {
		return err
	}
	// #nosec G704 -- Only the validated local readiness endpoint can be reached.
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("readiness endpoint unavailable")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return errors.New("runtime is not ready")
	}
	return nil
}
