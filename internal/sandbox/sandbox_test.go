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

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/sandbox"
)

func TestScriptParallelReads(t *testing.T) {
	t.Parallel()
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
		out, err := (sandbox.Inline{}).Execute(
			ctx,
			`const values=await Promise.all([modules.example.echo({n:1}),modules.example.echo({n:2})]); return values;`,
			[]sandbox.Binding{{Module: "example", Methods: []string{"echo"}}},
			broker,
		)
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
	t.Parallel()
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
	t.Parallel()
	_, err := (sandbox.Inline{}).Execute(context.Background(), "return 'a'.repeat(1048577);", nil, nil)
	require.EqualError(t, err, "script_result_too_large")
}
func TestScriptCallLimit(t *testing.T) {
	t.Parallel()
	broker := sandbox.BrokerFunc(func(context.Context, string, string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage("{}"), nil
	})
	_, err := (sandbox.Inline{}).Execute(
		context.Background(),
		"for(let i=0;i<129;i++) await modules.example.echo({}); return true;",
		[]sandbox.Binding{{Module: "example", Methods: []string{"echo"}}},
		broker,
	)
	require.EqualError(t, err, "script_call_limit")
}

// This test binary also acts as the sandbox child for the real process boundary test.
//
//nolint:paralleltest // The test binary uses this entry point as the isolated child process.
func TestWorkerProcess(_ *testing.T) {
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
	directory := t.TempDir()
	socket := filepath.Join(directory, "worker.sock")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	supervisor := sandbox.Supervisor{
		WorkerPath: executable,
		WorkerArgs: []string{"-test.run=^TestWorkerProcess$", "sandbox-worker"},
		MaxWorkers: 1,
	}
	stopped := make(chan error, 1)
	go func() { stopped <- supervisor.Serve(ctx, listener) }()
	client := sandbox.Client{Socket: socket, Timeout: time.Second}
	var calls atomic.Int32
	broker := sandbox.BrokerFunc(func(context.Context, string, string, json.RawMessage) (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(`{"ok":true}`), nil
	})
	result, err := client.Execute(
		context.Background(),
		"return await modules.example.echo({});",
		[]sandbox.Binding{{Module: "example", Methods: []string{"echo"}}},
		broker,
	)
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
	case stopErr := <-stopped:
		require.NoError(t, stopErr)
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor did not stop")
	}
}
func TestScriptSizeLimit(t *testing.T) {
	t.Parallel()
	_, err := (sandbox.Inline{}).Execute(context.Background(), strings.Repeat(" ", 65537), nil, nil)
	require.EqualError(t, err, "script_too_large")
}

func TestSupervisorKillsUnresponsiveChild(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Linux deployment process boundary")
	}
	directory := t.TempDir()
	socket := filepath.Join(directory, "worker.sock")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	supervisor := sandbox.Supervisor{
		WorkerPath: executable,
		WorkerArgs: []string{"-test.run=^TestWorkerProcess$", "sandbox-stuck"},
	}
	stopped := make(chan error, 1)
	go func() { stopped <- supervisor.Serve(ctx, listener) }()
	runCtx, runCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer runCancel()
	_, err = (sandbox.Client{Socket: socket, Timeout: time.Second}).Execute(runCtx, "return true;", nil, nil)
	require.Error(t, err)
	start := time.Now()
	cancel()
	select {
	case stopErr := <-stopped:
		require.NoError(t, stopErr)
		require.Less(t, time.Since(start), 2*time.Second)
	case <-time.After(2 * time.Second):
		t.Fatal("unresponsive child exceeded stop budget")
	}
}

func TestSupervisorHealthStartsNoWorker(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Linux Unix IPC")
	}
	socket := filepath.Join(t.TempDir(), "health.sock")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan error, 1)
	go func() {
		stopped <- (sandbox.Supervisor{WorkerPath: "/nonexistent/no-worker", MaxWorkers: 1}).Serve(ctx, listener)
	}()
	client := sandbox.Client{Socket: socket, Timeout: time.Second}
	require.NoError(t, client.Health(ctx))
	_, err = client.Execute(ctx, "return true;", nil, nil)
	require.Error(t, err)
	require.NoError(t, client.Health(ctx))
	cancel()
	require.NoError(t, <-stopped)
}

// A saturated supervisor still reads the request before it sends a response.
func TestSupervisorHealthAtCapacityWaitsForRequest(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Linux Unix IPC")
	}
	socket := filepath.Join(t.TempDir(), "capacity.sock")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stopped := make(chan error, 1)
	go func() {
		stopped <- (sandbox.Supervisor{WorkerPath: "/nonexistent/no-worker", MaxWorkers: 1}).Serve(ctx, listener)
	}()
	occupied, err := net.Dial("unix", socket)
	require.NoError(t, err)
	defer func() { _ = occupied.Close() }()
	probe, err := net.Dial("unix", socket)
	require.NoError(t, err)
	defer func() { _ = probe.Close() }()
	require.NoError(t, probe.SetReadDeadline(time.Now().Add(20*time.Millisecond)))
	var buffer [1]byte
	_, err = probe.Read(buffer[:])
	var timeout net.Error
	require.ErrorAs(t, err, &timeout)
	require.True(t, timeout.Timeout(), "supervisor must wait for the request")
	require.NoError(t, probe.SetDeadline(time.Now().Add(time.Second)))
	_, err = probe.Write([]byte("{\"type\":\"ping\"}\n"))
	require.NoError(t, err)
	var reply struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.NewDecoder(probe).Decode(&reply))
	require.Equal(t, "pong", reply.Type)
	require.NoError(t, (sandbox.Client{Socket: socket}).Health(ctx))
	cancel()
	require.NoError(t, <-stopped)
}
