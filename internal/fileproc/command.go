package fileproc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
)

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

type boundedBuffer struct {
	bytes.Buffer

	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("command output exceeds limit")
	}
	return b.Buffer.Write(p)
}

// command runs a fixed executable directly. No shell or caller-supplied executable is allowed.
func command(ctx context.Context, name string, args ...string) ([]byte, error) {
	switch name {
	case "pdfinfo", "pdftotext", "pdftoppm", "pdfimages", "ffprobe", "ffmpeg":
	default:
		return nil, errors.New("unsupported executable")
	}
	// #nosec G702 -- Fixed executable allowlist above; arguments never pass through a shell.
	cmd := exec.CommandContext(ctx, name, args...)
	stdout := &boundedBuffer{limit: MaxTextBytes}
	stderr := &boundedBuffer{limit: MaxStderrBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s failed: %w", name, err)
	}
	return stdout.Bytes(), nil
}

var _ io.Writer = (*boundedBuffer)(nil)
