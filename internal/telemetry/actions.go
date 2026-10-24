package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/complynx/loom/internal/config"
)

type actionIDKey struct{}

const (
	defaultActionLogSizeMB = 32
	actionLogMiB           = 1 << 20
	actionLogMode          = 0600
)

func (t *Telemetry) ActionsEnabled() bool {
	if t.actions == nil {
		return false
	}
	t.actions.mu.Lock()
	defer t.actions.mu.Unlock()
	return !t.actions.failed && !t.actions.closed
}

type actionJournal struct {
	mu      sync.Mutex
	file    *os.File
	path    string
	size    int64
	maxSize int64
	backups int
	failed  bool
	closed  bool
}

// EnableActionLog opens an append-only journal. One process must own each path.
// Each record stays intact; a single oversized record can exceed the rotation size.
func (t *Telemetry) EnableActionLog(c config.ActionLog) (func() error, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if !c.Enabled {
		return func() error { return nil }, nil
	}
	if c.MaxSizeMB == 0 {
		c.MaxSizeMB = defaultActionLogSizeMB
	}
	j := &actionJournal{path: c.Path, maxSize: int64(c.MaxSizeMB) * actionLogMiB, backups: c.MaxBackups}
	if err := j.open(); err != nil {
		return nil, errors.New("cannot open action log")
	}
	t.actions = j
	return func() error {
		j.mu.Lock()
		defer j.mu.Unlock()
		j.closed = true
		if j.file == nil {
			return nil
		}
		return j.file.Close()
	}, nil
}

func (j *actionJournal) open() error {
	info, err := os.Lstat(j.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && !info.Mode().IsRegular() {
		return errors.New("action log must be a regular file")
	}
	f, err := os.OpenFile(j.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, actionLogMode)
	if err != nil {
		return err
	}
	if err = f.Chmod(actionLogMode); err != nil {
		_ = f.Close()
		return err
	}
	info, err = f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	j.file, j.size = f, info.Size()
	return nil
}

func (j *actionJournal) rotate() error {
	if err := j.file.Close(); err != nil {
		return err
	}
	j.file = nil
	if j.backups == 0 {
		if err := os.Remove(j.path); err != nil {
			return err
		}
		return j.open()
	}
	oldest := fmt.Sprintf("%s.%d", j.path, j.backups)
	if err := os.Remove(oldest); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for n := j.backups - 1; n >= 1; n-- {
		if err := os.Rename(
			fmt.Sprintf("%s.%d", j.path, n),
			fmt.Sprintf("%s.%d", j.path, n+1),
		); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.Rename(j.path, j.path+".1"); err != nil {
		return err
	}
	return j.open()
}

func (j *actionJournal) write(raw []byte) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.failed || j.closed {
		return nil
	}
	if j.size > 0 && int64(len(raw)) > j.maxSize-j.size {
		if err := j.rotate(); err != nil {
			j.failed = true
			return err
		}
	}
	n, err := j.file.Write(raw)
	j.size += int64(n)
	if err == nil && n != len(raw) {
		err = io.ErrShortWrite
	}
	if err != nil {
		j.failed = true
	}
	return err
}

// Action records redacted structured data without the debug payload size limit.
// Journal failures disable further writes and report once; execution continues.
func (t *Telemetry) Action(ctx context.Context, event string, data any) {
	if !t.ActionsEnabled() {
		return
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.logger.WarnContext(ctx, "action log serialization failed")
		return
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err = decoder.Decode(&value); err != nil {
		return
	}
	actionID, _ := ctx.Value(actionIDKey{}).(string)
	raw, err = json.Marshal(map[string]any{
		"version": 1, "time": time.Now().UTC(), "event": event,
		"run_id": RunID(ctx), "request_id": RequestID(ctx), "action_id": actionID,
		"data": redactValue(value),
	})
	if err != nil {
		return
	}
	if err = t.actions.write(append(raw, '\n')); err != nil {
		t.logger.ErrorContext(ctx, "action log disabled after write failure")
	}
}

// BeginAction connects nested and parallel actions without using their order in a file.
// Completion records never include raw error strings or credentials.
func (t *Telemetry) BeginAction(ctx context.Context, kind string, input any) (context.Context, func(any, error)) {
	if !t.ActionsEnabled() {
		return ctx, func(any, error) {}
	}
	parent, _ := ctx.Value(actionIDKey{}).(string)
	ctx = context.WithValue(ctx, actionIDKey{}, uuid.NewString())
	t.Action(ctx, "action.started", map[string]any{"kind": kind, "parent_action_id": parent, "input": input})
	started := time.Now()
	return ctx, func(output any, err error) {
		outcome := "success"
		switch {
		case errors.Is(err, context.Canceled):
			outcome = "cancelled"
		case errors.Is(err, context.DeadlineExceeded):
			outcome = "timeout"
		case err != nil:
			outcome = "error"
		}
		t.Action(ctx, "action.completed", map[string]any{"kind": kind, "outcome": outcome,
			"duration_ms": time.Since(started).Milliseconds(), "output": output})
	}
}
