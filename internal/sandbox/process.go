package sandbox

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os/exec"
	"sync"
	"time"
)

const (
	frameExecute          = "execute"
	framePing             = "ping"
	framePong             = "pong"
	frameReply            = "reply"
	frameResult           = "result"
	frameError            = "error"
	defaultTimeout        = 30 * time.Second
	maxTimeout            = 5 * time.Minute
	requestTimeout        = 5 * time.Second
	cleanupWriteTimeout   = 100 * time.Millisecond
	defaultCleanupTimeout = 500 * time.Millisecond
)

type frame struct {
	Type      string          `json:"type"`
	ID        uint64          `json:"id,omitempty"`
	Source    string          `json:"source,omitempty"`
	Bindings  []Binding       `json:"bindings,omitempty"`
	Module    string          `json:"module,omitempty"`
	Method    string          `json:"method,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	Error     string          `json:"error,omitempty"`
	TimeoutMS int64           `json:"timeout_ms,omitempty"`
}
type wire struct {
	r  *bufio.Scanner
	w  io.Writer
	mu sync.Mutex
}

func newWire(r io.Reader, w io.Writer) *wire {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, frameBufferBytes), MaxFrameBytes)
	return &wire{r: s, w: w}
}
func (w *wire) send(f frame) error {
	b, e := json.Marshal(f)
	if e != nil {
		return e
	}
	if len(b) >= MaxFrameBytes {
		return errors.New("IPC frame too large")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_, e = w.w.Write(append(b, '\n'))
	return e
}
func (w *wire) read() (frame, error) {
	var f frame
	if !w.r.Scan() {
		if e := w.r.Err(); e != nil {
			return f, e
		}
		return f, io.EOF
	}
	e := json.Unmarshal(w.r.Bytes(), &f)
	return f, e
}

// Client connects only to the supervisor's private Unix socket.
type Client struct {
	Socket  string
	Timeout time.Duration
}

// Health checks supervisor IPC without starting a JavaScript child process.
func (c Client) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	wire := newWire(conn, conn)
	if sendErr := wire.send(frame{Type: framePing}); sendErr != nil {
		return sendErr
	}
	reply, err := wire.read()
	if err != nil {
		return err
	}
	if reply.Type == frameError && reply.Error == "sandbox capacity unavailable" {
		return nil
	}
	if reply.Type != framePong {
		return errors.New("sandbox unavailable or at capacity")
	}
	return nil
}

func (c Client) Execute(
	ctx context.Context,
	source string,
	bindings []Binding,
	broker Broker,
) (json.RawMessage, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	w := newWire(conn, conn)
	go func() { <-ctx.Done(); _ = conn.Close() }()
	if err = w.send(
		frame{Type: frameExecute, Source: source, Bindings: bindings, TimeoutMS: timeout.Milliseconds()},
	); err != nil {
		return nil, err
	}
	calls := 0
	slots := make(chan struct{}, maxParallelCalls)
	for {
		f, e := w.read()
		if e != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, e
		}
		switch f.Type {
		case "call":
			calls++
			if calls > maxCalls {
				return nil, errors.New("worker call limit exceeded")
			}
			select {
			case slots <- struct{}{}:
			default:
				_ = w.send(frame{Type: frameReply, ID: f.ID, Error: "parallel call limit exceeded"})
				continue
			}
			go respondToCall(ctx, broker, w, slots, f)
		case frameResult:
			return f.Data, nil
		case frameError:
			return nil, errors.New(f.Error)
		default:
			return nil, errors.New("invalid worker message")
		}
	}
}

// WorkerMain serves one execution. The supervisor must provide a credential-free process.
func WorkerMain(ctx context.Context, in io.Reader, out io.Writer) error {
	w := newWire(in, out)
	request, err := w.read()
	if err != nil {
		return err
	}
	if request.Type != frameExecute {
		return errors.New("execute required")
	}
	timeout := time.Duration(request.TimeoutMS) * time.Millisecond
	if timeout <= 0 || timeout > maxTimeout {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	transport := &workerTransport{wire: w, pending: map[uint64]chan frame{}}
	go transport.receive(ctx, cancel)
	broker := BrokerFunc(transport.call)
	result, err := (Inline{}).Execute(ctx, request.Source, request.Bindings, broker)
	if err != nil {
		return w.send(frame{Type: frameError, Error: err.Error()})
	}
	return w.send(frame{Type: frameResult, Data: result})
}

type Supervisor struct {
	Logger         *slog.Logger
	WorkerPath     string
	WorkerArgs     []string
	MaxWorkers     int
	CleanupTimeout time.Duration
}

func (s Supervisor) Serve(ctx context.Context, listener net.Listener) error {
	if s.CleanupTimeout < 0 || s.CleanupTimeout > time.Second {
		return errors.New("sandbox cleanup timeout must be at most one second")
	}
	ctx, cancel := context.WithCancel(ctx)
	count := s.MaxWorkers
	if count <= 0 {
		count = 4
	}
	slots := make(chan struct{}, count)
	var workers sync.WaitGroup
	go func() { <-ctx.Done(); _ = listener.Close() }()
	defer func() { cancel(); workers.Wait() }()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case slots <- struct{}{}:
			workers.Go(func() {
				defer func() { <-slots }()
				defer func() { _ = conn.Close() }()
				s.serveOne(ctx, conn)
			})
		default:
			s.rejectCapacity(ctx, conn)
			_ = conn.Close()
		}
	}
}

// Finish the handshake before closing a saturated connection. Otherwise the
// client can receive a broken pipe while sending a health check.
func (s Supervisor) rejectCapacity(ctx context.Context, conn net.Conn) {
	defer closeOnCancel(ctx, conn)()
	_ = conn.SetDeadline(time.Now().Add(cleanupWriteTimeout))
	client := newWire(conn, conn)
	request, err := client.read()
	if err != nil {
		return
	}
	if request.Type == framePing {
		_ = client.send(frame{Type: framePong})
		return
	}
	_ = client.send(frame{Type: frameError, Error: "sandbox capacity unavailable"})
}

func (s Supervisor) serveOne(ctx context.Context, conn net.Conn) {
	defer closeOnCancel(ctx, conn)()
	_ = conn.SetDeadline(time.Now().Add(requestTimeout))
	client := newWire(conn, conn)
	request, err := client.read()
	if err != nil {
		return
	}
	if request.Type == framePing {
		_ = client.send(frame{Type: framePong})
		return
	}
	if request.Type != frameExecute {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	timeout := time.Duration(request.TimeoutMS) * time.Millisecond
	if timeout <= 0 || timeout > maxTimeout {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := s.WorkerArgs
	if len(args) == 0 {
		args = []string{"sandbox-worker"}
	}
	// #nosec G204 -- Executable and arguments are operator configuration, never script input.
	cmd := exec.CommandContext(ctx, s.WorkerPath, args...)
	// Cancellation is relayed through IPC before the bounded cleanup kills the child.
	cmd.Cancel = func() error { return nil }
	cmd.Env = []string{}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		if s.Logger != nil {
			s.Logger.ErrorContext(
				ctx,
				"sandbox worker unavailable",
				"event",
				"sandbox.worker_start_failed",
				"component",
				"sandbox",
			)
		}
		_ = client.send(frame{Type: frameError, Error: "sandbox worker unavailable"})
		return
	}
	worker := newWire(stdout, stdin)
	if err = worker.send(request); err != nil {
		s.logKilled(ctx, cmd)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return
	}
	exited := make(chan error, 1)
	go relayWorker(worker, client, exited)
	go relayClient(client, worker, cancel)
	select {
	case <-exited:
	case <-ctx.Done():
		_ = worker.send(frame{Type: "cancel"})
		_ = conn.SetWriteDeadline(time.Now().Add(min(s.cleanupTimeout(), cleanupWriteTimeout)))
		_ = client.send(frame{Type: frameError, Error: fmt.Sprintf("sandbox stopped: %v", ctx.Err())})
	}
	s.waitForChild(ctx, stdin, cmd)
}

func (s Supervisor) cleanupTimeout() time.Duration {
	if s.CleanupTimeout <= 0 {
		return defaultCleanupTimeout
	}
	return s.CleanupTimeout
}

func respondToCall(ctx context.Context, broker Broker, w *wire, slots chan struct{}, f frame) {
	defer func() { <-slots }()
	data, err := broker.Call(ctx, f.Module, f.Method, f.Data)
	if len(data) > MaxValueBytes {
		data = nil
		err = errors.New("module result too large")
	}
	reply := frame{Type: frameReply, ID: f.ID, Data: data}
	if err != nil {
		reply.Error = err.Error()
		reply.Data = nil
	}
	if sendErr := w.send(reply); sendErr != nil {
		_ = w.send(frame{Type: frameReply, ID: f.ID, Error: "invalid module response"})
	}
}

type workerTransport struct {
	wire    *wire
	mu      sync.Mutex
	next    uint64
	pending map[uint64]chan frame
}

func (t *workerTransport) receive(ctx context.Context, cancel context.CancelFunc) {
	for {
		f, err := t.wire.read()
		if err != nil || f.Type != frameReply {
			cancel()
			return
		}
		t.mu.Lock()
		ch := t.pending[f.ID]
		t.mu.Unlock()
		if ch != nil {
			select {
			case ch <- f:
			case <-ctx.Done():
				return
			}
		}
	}
}
func (t *workerTransport) call(ctx context.Context, m, n string, a json.RawMessage) (json.RawMessage, error) {
	t.mu.Lock()
	t.next++
	id := t.next
	ch := make(chan frame, 1)
	t.pending[id] = ch
	t.mu.Unlock()
	defer func() { t.mu.Lock(); delete(t.pending, id); t.mu.Unlock() }()
	if err := t.wire.send(frame{Type: "call", ID: id, Module: m, Method: n, Data: a}); err != nil {
		return nil, err
	}
	select {
	case f := <-ch:
		if f.Error != "" {
			return nil, errors.New(f.Error)
		}
		return f.Data, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func relayWorker(worker, client *wire, exited chan<- error) {
	for {
		f, err := worker.read()
		if err != nil {
			exited <- err
			return
		}
		if err = client.send(f); err != nil {
			exited <- err
			return
		}
		if f.Type == frameResult || f.Type == frameError {
			exited <- nil
			return
		}
	}
}
func relayClient(client, worker *wire, cancel context.CancelFunc) {
	for {
		f, err := client.read()
		if err != nil || f.Type != frameReply {
			cancel()
			return
		}
		if err = worker.send(f); err != nil {
			cancel()
			return
		}
	}
}

func (s Supervisor) waitForChild(ctx context.Context, stdin io.Closer, cmd *exec.Cmd) {
	_ = stdin.Close()
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case <-waited:
	case <-time.After(s.cleanupTimeout()):
		s.logKilled(ctx, cmd)
		_ = cmd.Process.Kill()
		<-waited
	}
}

func (s Supervisor) logKilled(ctx context.Context, cmd *exec.Cmd) {
	if s.Logger != nil {
		s.Logger.WarnContext(
			ctx,
			"sandbox worker killed",
			"event",
			"sandbox.worker_killed",
			"component",
			"sandbox",
			"pid",
			cmd.Process.Pid,
		)
	}
}

// closeOnCancel stops its watcher when the request finishes.
func closeOnCancel(ctx context.Context, conn net.Conn) func() {
	closed := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-closed:
		}
	}()
	return func() { close(closed) }
}
