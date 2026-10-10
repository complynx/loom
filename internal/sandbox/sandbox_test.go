package sandbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/complynx/clx-scripter/internal/sandbox"
	"github.com/stretchr/testify/require"
)

func TestScriptParallelReads(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	broker := sandbox.BrokerFunc(func(ctx context.Context, _, _ string, args json.RawMessage) (json.RawMessage, error) {
		entered <- struct{}{}
		select {
		case <-release:
			return args, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	result := make(chan json.RawMessage, 1)
	errs := make(chan error, 1)
	go func() {
		out, err := (sandbox.Inline{}).Execute(ctx, `const values=await Promise.all([modules.example.echo({n:1}),modules.example.echo({n:2})]); return values;`, []sandbox.Binding{{Module: "example", Methods: []string{"echo"}}}, broker)
		result <- out
		errs <- err
	}()
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("calls did not start in parallel")
		}
	}
	close(release)
	require.NoError(t, <-errs)
	require.JSONEq(t, `[{"n":1},{"n":2}]`, string(<-result))
}
func TestScriptCannotAccessHostAndStops(t *testing.T) {
	broker := sandbox.BrokerFunc(func(context.Context, string, string, json.RawMessage) (json.RawMessage, error) {
		return nil, errors.New("unexpected call")
	})
	for _, source := range []string{"return require('fs');", "return process.env;"} {
		_, err := (sandbox.Inline{}).Execute(context.Background(), source, nil, broker)
		require.Error(t, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := (sandbox.Inline{}).Execute(ctx, "while(true){}", nil, broker)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
func TestScriptRejectsLargeResult(t *testing.T) {
	_, err := (sandbox.Inline{}).Execute(context.Background(), "return 'a'.repeat(1048577);", nil, nil)
	require.ErrorContains(t, err, "result too large")
}
func TestScriptCallLimit(t *testing.T) {
	broker := sandbox.BrokerFunc(func(context.Context, string, string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage("{}"), nil
	})
	_, err := (sandbox.Inline{}).Execute(context.Background(), "for(let i=0;i<129;i++) await modules.example.echo({}); return true;", []sandbox.Binding{{Module: "example", Methods: []string{"echo"}}}, broker)
	require.ErrorContains(t, err, "call limit")
}

// This test binary also acts as the sandbox child for the real process boundary test.
func TestWorkerProcess(t *testing.T) {
	if os.Args[len(os.Args)-1] == "sandbox-stuck" {
		for {
			time.Sleep(time.Hour)
		}
	}
	if os.Args[len(os.Args)-1] != "sandbox-worker" {
		return
	}
	if os.Getenv("CLX_TEST_SECRET") != "" {
		os.Exit(12)
	}
	if err := sandbox.WorkerMain(context.Background(), os.Stdin, os.Stdout); err != nil {
		os.Exit(13)
	}
	os.Exit(0)
}
func TestSupervisorRunsCredentialFreeChildAndCancels(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("target Unix-socket supervisor is verified on Linux")
	}
	t.Setenv("CLX_TEST_SECRET", "must-not-reach-child")
	directory, err := os.MkdirTemp("", "clx-sandbox-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "worker.sock")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	supervisor := sandbox.Supervisor{WorkerPath: executable, WorkerArgs: []string{"-test.run=^TestWorkerProcess$", "sandbox-worker"}, MaxWorkers: 1}
	stopped := make(chan error, 1)
	go func() { stopped <- supervisor.Serve(ctx, listener) }()
	client := sandbox.Client{Socket: socket, Timeout: time.Second}
	var calls atomic.Int32
	broker := sandbox.BrokerFunc(func(context.Context, string, string, json.RawMessage) (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(`{"ok":true}`), nil
	})
	result, err := client.Execute(context.Background(), "return await modules.example.echo({});", []sandbox.Binding{{Module: "example", Methods: []string{"echo"}}}, broker)
	require.NoError(t, err)
	require.JSONEq(t, `{"ok":true}`, string(result))
	require.EqualValues(t, 1, calls.Load())
	runCtx, runCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer runCancel()
	start := time.Now()
	_, err = client.Execute(runCtx, "while(true){}", nil, broker)
	require.Error(t, err)
	require.Less(t, time.Since(start), 2*time.Second)
	cancel()
	select {
	case err := <-stopped:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor did not stop")
	}
}
func TestScriptSizeLimit(t *testing.T) {
	_, err := (sandbox.Inline{}).Execute(context.Background(), strings.Repeat(" ", 65537), nil, nil)
	require.ErrorContains(t, err, "script exceeds")
}

func TestSupervisorKillsUnresponsiveChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux deployment process boundary")
	}
	directory, err := os.MkdirTemp("", "clx-stuck-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "worker.sock")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	supervisor := sandbox.Supervisor{WorkerPath: executable, WorkerArgs: []string{"-test.run=^TestWorkerProcess$", "sandbox-stuck"}}
	stopped := make(chan error, 1)
	go func() { stopped <- supervisor.Serve(ctx, listener) }()
	runCtx, runCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer runCancel()
	_, err = (sandbox.Client{Socket: socket, Timeout: time.Second}).Execute(runCtx, "return true;", nil, nil)
	require.Error(t, err)
	start := time.Now()
	cancel()
	select {
	case err := <-stopped:
		require.NoError(t, err)
		require.Less(t, time.Since(start), 2*time.Second)
	case <-time.After(2 * time.Second):
		t.Fatal("unresponsive child exceeded stop budget")
	}
}
