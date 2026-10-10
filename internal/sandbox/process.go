package sandbox

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"sync"
	"time"
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
	s.Buffer(make([]byte, 4096), MaxFrameBytes)
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

func (c Client) Execute(ctx context.Context, source string, bindings []Binding, broker Broker) (json.RawMessage, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
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
	if err = w.send(frame{Type: "execute", Source: source, Bindings: bindings, TimeoutMS: timeout.Milliseconds()}); err != nil {
		return nil, err
	}
	calls := 0
	slots := make(chan struct{}, 8)
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
			if calls > 128 {
				return nil, errors.New("worker call limit exceeded")
			}
			select {
			case slots <- struct{}{}:
			default:
				_ = w.send(frame{Type: "reply", ID: f.ID, Error: "parallel call limit exceeded"})
				continue
			}
			go func(f frame) {
				defer func() { <-slots }()
				data, e := broker.Call(ctx, f.Module, f.Method, f.Data)
				if len(data) > MaxValueBytes {
					data = nil
					e = errors.New("module result too large")
				}
				reply := frame{Type: "reply", ID: f.ID, Data: data}
				if e != nil {
					reply.Error = e.Error()
					reply.Data = nil
				}
				if err := w.send(reply); err != nil {
					_ = w.send(frame{Type: "reply", ID: f.ID, Error: "invalid module response"})
				}
			}(f)
		case "result":
			return f.Data, nil
		case "error":
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
	if request.Type != "execute" {
		return errors.New("execute required")
	}
	timeout := time.Duration(request.TimeoutMS) * time.Millisecond
	if timeout <= 0 || timeout > 5*time.Minute {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var mu sync.Mutex
	next := uint64(0)
	pending := map[uint64]chan frame{}
	go func() {
		for {
			f, e := w.read()
			if e != nil {
				cancel()
				return
			}
			if f.Type == "cancel" {
				cancel()
				return
			}
			if f.Type != "reply" {
				cancel()
				return
			}
			mu.Lock()
			ch := pending[f.ID]
			mu.Unlock()
			if ch != nil {
				select {
				case ch <- f:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	broker := BrokerFunc(func(ctx context.Context, m, n string, a json.RawMessage) (json.RawMessage, error) {
		mu.Lock()
		next++
		id := next
		ch := make(chan frame, 1)
		pending[id] = ch
		mu.Unlock()
		defer func() { mu.Lock(); delete(pending, id); mu.Unlock() }()
		if e := w.send(frame{Type: "call", ID: id, Module: m, Method: n, Data: a}); e != nil {
			return nil, e
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
	})
	result, err := (Inline{}).Execute(ctx, request.Source, request.Bindings, broker)
	if err != nil {
		return w.send(frame{Type: "error", Error: err.Error()})
	}
	return w.send(frame{Type: "result", Data: result})
}

type Supervisor struct {
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
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer func() { <-slots }()
				defer func() { _ = conn.Close() }()
				s.serveOne(ctx, conn)
			}()
		default:
			_ = newWire(conn, conn).send(frame{Type: "error", Error: "sandbox capacity unavailable"})
			_ = conn.Close()
		}
	}
}
func (s Supervisor) serveOne(ctx context.Context, conn net.Conn) {
	closed := make(chan struct{})
	defer close(closed)
	go func(parent context.Context) {
		select {
		case <-parent.Done():
			_ = conn.Close()
		case <-closed:
		}
	}(ctx)
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	client := newWire(conn, conn)
	request, err := client.read()
	if err != nil || request.Type != "execute" {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	timeout := time.Duration(request.TimeoutMS) * time.Millisecond
	if timeout <= 0 || timeout > 5*time.Minute {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := s.WorkerArgs
	if len(args) == 0 {
		args = []string{"sandbox-worker"}
	}
	// #nosec G204 -- Executable and arguments are operator configuration, never script input.
	cmd := exec.Command(s.WorkerPath, args...)
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
		_ = client.send(frame{Type: "error", Error: "sandbox worker unavailable"})
		return
	}
	worker := newWire(stdout, stdin)
	if err = worker.send(request); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return
	}
	exited := make(chan error, 1)
	go func() {
		for {
			f, e := worker.read()
			if e != nil {
				exited <- e
				return
			}
			if e = client.send(f); e != nil {
				exited <- e
				return
			}
			if f.Type == "result" || f.Type == "error" {
				exited <- nil
				return
			}
		}
	}()
	go func() {
		for {
			f, e := client.read()
			if e != nil {
				cancel()
				return
			}
			if f.Type != "reply" {
				cancel()
				return
			}
			if e = worker.send(f); e != nil {
				cancel()
				return
			}
		}
	}()
	select {
	case <-exited:
	case <-ctx.Done():
		_ = worker.send(frame{Type: "cancel"})
		_ = conn.SetWriteDeadline(time.Now().Add(min(s.cleanupTimeout(), 100*time.Millisecond)))
		_ = client.send(frame{Type: "error", Error: fmt.Sprintf("sandbox stopped: %v", ctx.Err())})
	}
	_ = stdin.Close()
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case <-waited:
	case <-time.After(s.cleanupTimeout()):
		_ = cmd.Process.Kill()
		<-waited
	}
}

func (s Supervisor) cleanupTimeout() time.Duration {
	if s.CleanupTimeout <= 0 {
		return 500 * time.Millisecond
	}
	return s.CleanupTimeout
}
