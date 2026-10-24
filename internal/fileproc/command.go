package fileproc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
)

const ffprobeCommand = "ffprobe"

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
	case "pdfinfo", "pdftotext", "pdftoppm", "pdfimages", ffprobeCommand, "ffmpeg", "loom-animation":
	default:
		return nil, errors.New("unsupported executable")
	}
	if name == "ffmpeg" {
		// Two concurrent jobs must fit the worker's PID budget on large hosts.
		args = append([]string{"-threads", "1", "-filter_threads", "1", "-filter_complex_threads", "1"}, args...)
		if len(args) > 0 {
			output := args[len(args)-1]
			args = append(args[:len(args)-1], "-threads", "1", output)
		}
	}
	if name == ffprobeCommand {
		args = append([]string{"-threads", "1"}, args...)
	}
	// #nosec G702 -- Fixed executable allowlist above; arguments never pass through a shell.
	cmd := exec.CommandContext(ctx, name, args...)
	stdout := &boundedBuffer{limit: MaxTextBytes}
	if name == ffprobeCommand {
		stdout.limit = MaxResultJSONBytes
	}
	stderr := &boundedBuffer{limit: MaxStderrBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s failed: %w", name, err)
	}
	// Poppler can exit successfully after dropping text with unresolved fonts.
	if name == "pdftoppm" && bytes.Contains(stderr.Bytes(), []byte("Couldn't find a font")) {
		return nil, errors.New("PDF rendering failed: a required font is unavailable")
	}
	return stdout.Bytes(), nil
}

var _ io.Writer = (*boundedBuffer)(nil)
