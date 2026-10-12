package fileproc_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/complynx/loom/internal/fileproc"
	"github.com/complynx/loom/internal/filetext"
)

func TestClientStreamsFilesOverUnixSocket(t *testing.T) {
	t.Parallel()
	socket := filepath.Join(t.TempDir(), "worker.sock")
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Skip("Unix sockets unavailable: " + err.Error())
	}
	handler := &fileproc.Handler{
		LocalProcessor: func(_ context.Context, _ fileproc.Request, input, dir string) (fileproc.Result, error) {
			data, fileErr := os.ReadFile(input)
			if fileErr != nil {
				return fileproc.Result{}, fileErr
			}
			path := filepath.Join(dir, "derived.txt")
			if fileErr = os.WriteFile(path, data, 0600); fileErr != nil {
				return fileproc.Result{}, fileErr
			}
			return fileproc.Result{
				Data:    json.RawMessage(`{"ok":true}`),
				Outputs: []fileproc.Output{{Name: "derived.txt", MIME: "text/plain", Path: path}},
			}, nil
		},
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	client := fileproc.Client{Socket: socket}
	if err = client.Health(t.Context()); err != nil {
		t.Fatal(err)
	}
	expected := strings.Repeat("abcdef", 4096)
	actual := ""
	result, err := client.Process(
		t.Context(),
		fileproc.Request{Operation: "test"},
		strings.NewReader(expected),
		func(out fileproc.Output, reader io.Reader) error {
			if out.Path != "" {
				t.Fatal("worker path leaked")
			}
			b, readErr := io.ReadAll(reader)
			actual = string(b)
			return readErr
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if actual != expected || len(result.Outputs) != 1 {
		t.Fatal("streamed output mismatch")
	}
}

func TestClientPreservesEscapedTextPages(t *testing.T) {
	t.Parallel()
	for _, character := range []string{"<", "\t"} {
		t.Run(
			map[string]string{"<": "angle", "\t": "tabs"}[character],
			func(t *testing.T) { t.Parallel(); assertEscapedTextPages(t, character) },
		)
	}
}
func assertEscapedTextPages(t *testing.T, character string) {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "worker.sock")
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Skip("Unix sockets unavailable: " + err.Error())
	}
	handler := &fileproc.Handler{
		LocalProcessor: func(ctx context.Context, request fileproc.Request, input, dir string) (fileproc.Result, error) {
			data, _, partial, cursor, processErr := filetext.Process(
				ctx,
				request.Operation,
				request.Params,
				input,
				dir,
			)
			return fileproc.Result{Data: data, Partial: partial, NextCursor: cursor}, processErr
		},
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	client := fileproc.Client{Socket: socket}
	source := strings.Repeat(character, 32770)
	first, err := client.Process(
		t.Context(),
		fileproc.Request{Operation: "text.read", Params: json.RawMessage(`{"limit":32768}`)},
		strings.NewReader(source),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Text string `json:"text"`
	}
	if err = json.Unmarshal(first.Data, &page); err != nil {
		t.Fatal(err)
	}
	if len(first.Data) <= fileproc.MaxTextBytes || page.Text != source[:32768] || !first.Partial ||
		first.NextCursor != "32768" {
		t.Fatalf(
			"escaped first page lost: size=%d cursor=%q partial=%v",
			len(first.Data),
			first.NextCursor,
			first.Partial,
		)
	}
	second, err := client.Process(
		t.Context(),
		fileproc.Request{Operation: "text.read", Params: json.RawMessage(`{"limit":32768,"cursor":"32768"}`)},
		strings.NewReader(source),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(second.Data, &page); err != nil {
		t.Fatal(err)
	}
	if page.Text != source[32768:] || second.Partial {
		t.Fatal("tail page or cursor lost")
	}
}
