//go:build linux

package fileproc_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/complynx/loom/internal/fileproc"
)

func waitWorker(t *testing.T, client fileproc.Client, stopped <-chan error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		err := client.Health(ctx)
		if err == nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("worker did not become healthy: %v", err)
		case stopErr := <-stopped:
			t.Fatalf("worker stopped before readiness: %v", stopErr)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func TestWorkerReclaimsStaleSocket(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "worker.sock")
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	unixListener, ok := listener.(*net.UnixListener)
	if !ok {
		t.Fatal("Unix listener required")
	}
	unixListener.SetUnlinkOnClose(false)
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- (&fileproc.Handler{}).ListenAndServe(ctx, path) }()
	waitWorker(t, fileproc.Client{Socket: path}, done)
	cancel()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker shutdown exceeded one second")
	}
}
func TestSecondWorkerCannotReplaceActiveSocket(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "worker.sock")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- (&fileproc.Handler{}).ListenAndServe(ctx, path) }()
	client := fileproc.Client{Socket: path}
	waitWorker(t, client, done)
	if err := (&fileproc.Handler{}).ListenAndServe(t.Context(), path); err == nil {
		t.Fatal("second worker replaced active socket")
	}
	if err := client.Health(t.Context()); err != nil {
		t.Fatal("first worker was damaged:", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker shutdown exceeded one second")
	}
}
func TestWorkerRejectsUnsafeSocketPaths(t *testing.T) {
	t.Parallel()
	for _, symlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "regular", true: "symlink"}[symlink], func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "worker.sock")
			target := filepath.Join(dir, "target")
			if err := os.WriteFile(target, []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			makeUnsafeSocket(t, path, target, symlink)
			if err := (&fileproc.Handler{}).ListenAndServe(t.Context(), path); err == nil {
				t.Fatal("unsafe path accepted")
			}
			data, err := os.ReadFile(target)
			if err != nil || string(data) != "preserve" {
				t.Fatal("target changed")
			}
		})
	}
}

func makeUnsafeSocket(t *testing.T, path, target string, symlink bool) {
	t.Helper()
	if symlink {
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.WriteFile(path, []byte("preserve"), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
