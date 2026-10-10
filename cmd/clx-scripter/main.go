package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/complynx/clx-scripter/internal/app"
	"github.com/complynx/clx-scripter/internal/config"
	"github.com/complynx/clx-scripter/internal/sandbox"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "clx-scripter:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("expected serve, migrate, sandbox-supervisor, sandbox-worker or docker-proxy")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	switch args[0] {
	case "docker-proxy":
		flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
		listen := flags.String("listen", ":2375", "private Docker discovery proxy address")
		socket := flags.String("socket", "/var/run/docker.sock", "operator Docker socket")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || !filepath.IsAbs(*socket) {
			return errors.New("invalid Docker proxy configuration")
		}
		return app.ServeDockerProxy(ctx, *listen, *socket)
	case "sandbox-worker":
		if len(args) != 1 {
			return errors.New("sandbox-worker takes no arguments")
		}
		return sandbox.WorkerMain(ctx, os.Stdin, os.Stdout)
	case "sandbox-supervisor":
		flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
		socket := flags.String("socket", "/run/clx-sandbox/sandbox.sock", "private Unix socket")
		maxWorkers := flags.Int("max-workers", 4, "maximum concurrent child processes")
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		worker := flags.String("worker-executable", executable, "sandbox worker executable")
		cleanup := flags.Duration("cleanup-timeout", 100*time.Millisecond, "sandbox cancellation grace")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *maxWorkers < 1 || *maxWorkers > 32 || *cleanup <= 0 || *cleanup > time.Second || !filepath.IsAbs(*socket) || !filepath.IsAbs(*worker) {
			return errors.New("invalid sandbox supervisor configuration")
		}
		// #nosec G703 -- directory is from an absolute operator-only Unix socket path.
		if err := os.MkdirAll(filepath.Dir(*socket), 0750); err != nil {
			return err
		}
		// #nosec G703 -- absolute Unix socket path comes only from operator CLI flags.
		if info, statErr := os.Lstat(*socket); statErr == nil {
			if info.Mode()&os.ModeSocket == 0 {
				return errors.New("sandbox socket path is occupied by a non-socket file")
			}
			// #nosec G704 -- Unix-domain probe of the validated operator socket, never an HTTP URL.
			probe, probeErr := net.DialTimeout("unix", *socket, 200*time.Millisecond)
			if probeErr == nil {
				_ = probe.Close()
				return errors.New("sandbox supervisor is already running")
			}
			if !errors.Is(probeErr, syscall.ECONNREFUSED) && !errors.Is(probeErr, os.ErrNotExist) {
				return fmt.Errorf("cannot prove sandbox socket is stale: %w", probeErr)
			}
			// #nosec G703 -- only a verified stale socket at the operator absolute path is removed.
			if err := os.Remove(*socket); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		listener, err := net.Listen("unix", *socket)
		if err != nil {
			return fmt.Errorf("listen on private sandbox socket: %w", err)
		}
		defer func() { _ = listener.Close() }()
		// #nosec G703 -- restrict permissions on the listener created at the validated operator path.
		if err := os.Chmod(*socket, 0600); err != nil {
			return err
		}
		return (sandbox.Supervisor{WorkerPath: *worker, WorkerArgs: []string{"sandbox-worker"}, MaxWorkers: *maxWorkers, CleanupTimeout: *cleanup}).Serve(ctx, listener)
	case "serve", "migrate":
		flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
		configPath := os.Getenv("CLX_CONFIG")
		if configPath == "" {
			configPath = "config.yaml"
		}
		path := flags.String("config", configPath, "YAML configuration file")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("unexpected command arguments")
		}
		if args[0] == "migrate" {
			if dsn := os.Getenv("CLX_DATABASE_DSN"); dsn != "" {
				return app.Migrate(ctx, config.Config{Database: config.Database{DSN: dsn}})
			}
		}
		cfg, err := config.Load(*path)
		if err != nil {
			return fmt.Errorf("load configuration: %w", err)
		}
		if args[0] == "migrate" {
			return app.Migrate(ctx, cfg)
		}
		return app.Serve(ctx, cfg)
	default:
		return errors.New("unknown command")
	}
}
