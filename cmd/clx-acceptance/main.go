// Command clx-acceptance serves the isolated module acceptance fixture.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/complynx/loom/internal/acceptance"
)

const headerTimeout = 5 * time.Second
const requestTimeout = 10 * time.Second
const idleTimeout = 30 * time.Second

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(); err != nil {
		logger.Error("QA service stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	service, err := acceptance.New(
		os.Getenv("CLX_QA_STATE_PATH"),
		os.Getenv("CLX_QA_READ_TOKEN"),
		os.Getenv("CLX_QA_WRITE_TOKEN"),
	)
	if err != nil {
		return err
	}
	address := os.Getenv("CLX_QA_LISTEN")
	if address == "" {
		address = ":8090"
	}
	server := &http.Server{
		Addr:              address,
		Handler:           service.Handler(),
		ReadHeaderTimeout: headerTimeout,
		ReadTimeout:       requestTimeout,
		WriteTimeout:      requestTimeout,
		IdleTimeout:       idleTimeout,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result := make(chan error, 1)
	go func() { result <- server.ListenAndServe() }()
	select {
	case err = <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}
