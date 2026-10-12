package fileproc_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/complynx/loom/internal/fileproc"
)

func testRequest(t *testing.T, input string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormField("job")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte(`{"operation":"test","params":{}}`))
	part, err = writer.CreateFormFile("input", "untrusted.pdf")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(part, strings.NewReader(input))
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/process", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}
func TestHandlerStreamsDerivedFile(t *testing.T) {
	t.Parallel()
	handler := &fileproc.Handler{
		LocalProcessor: func(_ context.Context, _ fileproc.Request, input, dir string) (fileproc.Result, error) {
			b, err := os.ReadFile(input)
			if err != nil {
				return fileproc.Result{}, err
			}
			path := filepath.Join(dir, "output.txt")
			if err = os.WriteFile(path, append([]byte("derived:"), b...), 0600); err != nil {
				return fileproc.Result{}, err
			}
			return fileproc.Result{
				Data:    json.RawMessage(`{"ok":true}`),
				Outputs: []fileproc.Output{{Name: "output.txt", MIME: "text/plain", Path: path}},
			}, nil
		},
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, testRequest(t, "hello"))
	if response.Code != 200 {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "derived:hello") {
		t.Fatal("missing streamed file")
	}
	if strings.Contains(response.Body.String(), "loom-file-") {
		t.Fatal("local paths leaked")
	}
}
func TestHandlerRejectsInputBeforeProcessing(t *testing.T) {
	t.Parallel()
	called := false
	handler := &fileproc.Handler{
		Limits: fileproc.Limits{InputBytes: 3},
		LocalProcessor: func(context.Context, fileproc.Request, string, string) (fileproc.Result, error) {
			called = true
			return fileproc.Result{}, nil
		},
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, testRequest(t, "oversized"))
	if response.Code != 400 || called {
		t.Fatalf("status %d called=%v", response.Code, called)
	}
}
func TestHandlerRejectsEscapingOutput(t *testing.T) {
	t.Parallel()
	handler := &fileproc.Handler{
		LocalProcessor: func(context.Context, fileproc.Request, string, string) (fileproc.Result, error) {
			return fileproc.Result{Outputs: []fileproc.Output{{Name: "secret", Path: "../secret"}}}, nil
		},
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, testRequest(t, "hello"))
	if response.Code != 422 {
		t.Fatalf("status %d", response.Code)
	}
}
func TestHandlerCancelsProcessor(t *testing.T) {
	t.Parallel()
	handler := &fileproc.Handler{
		Limits: fileproc.Limits{Timeout: time.Millisecond},
		LocalProcessor: func(ctx context.Context, _ fileproc.Request, _, _ string) (fileproc.Result, error) {
			<-ctx.Done()
			return fileproc.Result{}, ctx.Err()
		},
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, testRequest(t, "hello"))
	if response.Code != 422 {
		t.Fatalf("status %d", response.Code)
	}
}
func TestUnsupportedOperation(t *testing.T) {
	t.Parallel()
	if _, err := fileproc.Process(
		t.Context(),
		fileproc.Request{Operation: "shell.exec"},
		"input",
		t.TempDir(),
	); err == nil {
		t.Fatal("unsupported operation accepted")
	}
}
