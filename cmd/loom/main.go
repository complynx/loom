package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/complynx/loom/internal/app"
	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/sandbox"
)

const (
	commandMigrate           = "migrate"
	commandRotateSessionKeys = "rotate-session-keys"
	defaultMaxWorkers        = 4
	defaultCleanupTimeout    = 100 * time.Millisecond
	socketProbeTimeout       = 200 * time.Millisecond
	defaultRotationBatch     = 100
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "loom:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New(
			"expected serve, migrate, rotate-session-keys, sandbox-supervisor, sandbox-worker or docker-proxy",
		)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	switch args[0] {
	case "docker-proxy":
		return runDockerProxy(ctx, args)
	case "sandbox-worker":
		if len(args) != 1 {
			return errors.New("sandbox-worker takes no arguments")
		}
		return sandbox.WorkerMain(ctx, os.Stdin, os.Stdout)
	case "sandbox-supervisor":
		return runSandboxSupervisor(ctx, args)
	case "serve", commandMigrate, commandRotateSessionKeys:
		return runConfigured(ctx, args)
	default:
		return errors.New("unknown command")
	}
}

func runDockerProxy(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	listen := flags.String("listen", ":2375", "private Docker discovery proxy address")
	socket := flags.String("socket", "/var/run/docker.sock", "operator Docker socket")
	if parseErr := flags.Parse(args[1:]); parseErr != nil {
		return parseErr
	}
	if flags.NArg() != 0 || !filepath.IsAbs(*socket) {
		return errors.New("invalid Docker proxy configuration")
	}
	return app.ServeDockerProxy(ctx, *listen, *socket)
}

func runSandboxSupervisor(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	socket := flags.String("socket", "/run/clx-sandbox/sandbox.sock", "private Unix socket")
	maxWorkers := flags.Int("max-workers", defaultMaxWorkers, "maximum concurrent child processes")
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	worker := flags.String("worker-executable", executable, "sandbox worker executable")
	cleanup := flags.Duration("cleanup-timeout", defaultCleanupTimeout, "sandbox cancellation grace")
	if parseErr := flags.Parse(args[1:]); parseErr != nil {
		return parseErr
	}
	if flags.NArg() != 0 || *maxWorkers < 1 || *maxWorkers > 32 || *cleanup <= 0 || *cleanup > time.Second ||
		!filepath.IsAbs(*socket) ||
		!filepath.IsAbs(*worker) {
		return errors.New("invalid sandbox supervisor configuration")
	}
	// #nosec G703 -- directory is from an absolute operator-only Unix socket path.
	if err = os.MkdirAll(filepath.Dir(*socket), 0750); err != nil {
		return err
	}
	if err = removeStaleSocket(ctx, *socket); err != nil {
		return err
	}
	listenConfig := net.ListenConfig{}
	listener, err := listenConfig.Listen(ctx, "unix", *socket)
	if err != nil {
		return fmt.Errorf("listen on private sandbox socket: %w", err)
	}
	defer func() { _ = listener.Close() }()
	// #nosec G703 -- restrict permissions on the listener created at the validated operator path.
	if err = os.Chmod(*socket, 0600); err != nil {
		return err
	}
	return (sandbox.Supervisor{Logger: slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "loom-sandbox"), WorkerPath: *worker, WorkerArgs: []string{"sandbox-worker"}, MaxWorkers: *maxWorkers, CleanupTimeout: *cleanup}).Serve(
		ctx,
		listener,
	)
}

func runConfigured(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	configPath := os.Getenv("CLX_CONFIG")
	if configPath == "" {
		configPath = "config.yaml"
	}
	path := flags.String("config", configPath, "YAML configuration file")
	batch := defaultRotationBatch
	if args[0] == commandRotateSessionKeys {
		flags.IntVar(&batch, "batch-size", defaultRotationBatch, "session key rotation batch (1..1000)")
	}
	if parseErr := flags.Parse(args[1:]); parseErr != nil {
		return parseErr
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected command arguments")
	}
	if args[0] == commandMigrate {
		if dsn := os.Getenv("CLX_DATABASE_DSN"); dsn != "" {
			return app.Migrate(ctx, config.Config{Database: config.Database{DSN: dsn}})
		}
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	if args[0] == commandMigrate {
		return app.Migrate(ctx, cfg)
	}
	if args[0] == commandRotateSessionKeys {
		n, rotateErr := app.RotateSessionKeys(ctx, cfg, batch)
		if rotateErr != nil {
			return rotateErr
		}
		_, err = fmt.Fprintf(
			os.Stdout,
			"rotated_sessions=%d; repeat until zero, after all writers use the active key\n",
			n,
		)
		return err
	}
	return app.Serve(ctx, cfg)
}

func removeStaleSocket(ctx context.Context, socket string) error {
	// #nosec G703 -- absolute Unix socket path comes only from operator CLI flags.
	info, statErr := os.Lstat(socket)
	if errors.Is(statErr, os.ErrNotExist) {
		return nil
	}
	if statErr != nil {
		return statErr
	}
	if info.Mode()&os.ModeSocket == 0 {
		return errors.New("sandbox socket path is occupied by a non-socket file")
	}
	// #nosec G704 -- Unix-domain probe of the validated operator socket, never an HTTP URL.
	dialer := net.Dialer{Timeout: socketProbeTimeout}
	probe, probeErr := dialer.DialContext(ctx, "unix", socket)
	if probeErr == nil {
		_ = probe.Close()
		return errors.New("sandbox supervisor is already running")
	}
	if !errors.Is(probeErr, syscall.ECONNREFUSED) && !errors.Is(probeErr, os.ErrNotExist) {
		return fmt.Errorf("cannot prove sandbox socket is stale: %w", probeErr)
	}
	// #nosec G703 -- only a verified stale socket at the operator absolute path is removed.
	if removeErr := os.Remove(socket); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return removeErr
	}
	return nil
}
