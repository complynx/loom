package fileproc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

type Handler struct {
	Limits         Limits
	LocalProcessor LocalProcessor
	once           sync.Once
	slots          chan struct{}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/process" {
		http.NotFound(w, r)
		return
	}
	if err := h.Limits.Validate(); err != nil {
		http.Error(w, "invalid limits", http.StatusServiceUnavailable)
		return
	}
	limits := h.Limits.defaults()
	h.once.Do(func() { h.slots = make(chan struct{}, limits.Concurrency) })
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		http.Error(w, "processor busy", http.StatusTooManyRequests)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), limits.Timeout)
	defer cancel()
	r.Body = http.MaxBytesReader(w, r.Body, limits.InputBytes+MaxTextBytes)
	reader, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "multipart required", http.StatusBadRequest)
		return
	}
	dir, err := os.MkdirTemp("", "loom-file-")
	if err != nil {
		http.Error(w, "temporary storage unavailable", http.StatusServiceUnavailable)
		return
	}
	defer os.RemoveAll(dir)
	req, input, err := readJob(reader, dir, limits.InputBytes)
	if err != nil {
		http.Error(w, "invalid bounded file job", http.StatusBadRequest)
		return
	}
	process := h.LocalProcessor
	if process == nil {
		process = Process
	}
	result, err := process(ctx, req, input, dir)
	if err != nil {
		http.Error(w, "file processing failed", http.StatusUnprocessableEntity)
		return
	}
	if err = validateOutputs(result, dir, limits.OutputBytes); err != nil {
		http.Error(w, "file output exceeds limits", http.StatusUnprocessableEntity)
		return
	}
	writeResponse(w, result)
}
func writeResponse(w http.ResponseWriter, result Result) {
	resultJSON, err := json.Marshal(result)
	if err != nil {
		http.Error(w, "invalid result", http.StatusInternalServerError)
		return
	}
	writer := multipart.NewWriter(w)
	w.Header().Set("Content-Type", writer.FormDataContentType())
	part, err := writer.CreateFormField("result")
	if err != nil {
		return
	}
	if _, err = part.Write(resultJSON); err != nil {
		return
	}
	for _, out := range result.Outputs {
		file, openErr := os.Open(out.Path)
		if openErr != nil {
			return
		}
		part, err = writer.CreateFormFile(out.Name, out.Name)
		if err == nil {
			_, err = io.Copy(part, file)
		}
		_ = file.Close()
		if err != nil {
			return
		}
	}
	_ = writer.Close()
}

func readJob(reader *multipart.Reader, dir string, limit int64) (Request, string, error) {
	var req Request
	part, err := reader.NextPart()
	if err != nil {
		return req, "", err
	}
	if part.FormName() != "job" {
		return req, "", errors.New("job must be first")
	}
	b, err := io.ReadAll(io.LimitReader(part, MaxJobBytes+1))
	if err != nil || len(b) > MaxJobBytes {
		return req, "", errors.New("job too large")
	}
	if err = decodeOptions(b, &req); err != nil {
		return req, "", err
	}
	part, err = reader.NextPart()
	if err != nil {
		return req, "", err
	}
	if part.FormName() != "input" {
		return req, "", errors.New("input required")
	}
	path := filepath.Join(dir, "input")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return req, "", err
	}
	n, copyErr := io.Copy(file, io.LimitReader(part, limit+1))
	closeErr := file.Close()
	if copyErr != nil {
		return req, "", copyErr
	}
	if closeErr != nil {
		return req, "", closeErr
	}
	if n > limit {
		return req, "", errors.New("input too large")
	}
	if _, err = reader.NextPart(); !errors.Is(err, io.EOF) {
		return req, "", errors.New("extra part")
	}
	return req, path, nil
}
func validateOutputs(result Result, dir string, limit int64) error {
	if len(result.Data) > MaxResultJSONBytes || len(result.Outputs) > MaxFrames {
		return errors.New("result too large")
	}
	names := map[string]bool{}
	var size int64
	for _, out := range result.Outputs {
		rel, err := filepath.Rel(dir, out.Path)
		if err != nil || rel == ".." || filepath.IsAbs(rel) ||
			len(rel) > 3 && rel[:3] == ".."+string(filepath.Separator) {
			return errors.New("output outside temporary directory")
		}
		if out.Name == "" || filepath.Base(out.Name) != out.Name || names[out.Name] {
			return errors.New("invalid output name")
		}
		names[out.Name] = true
		info, err := os.Lstat(out.Path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("output must be regular file")
		}
		size += info.Size()
		if size > limit {
			return errors.New("output too large")
		}
	}
	return nil
}
func (h *Handler) ListenAndServe(ctx context.Context, socket string) error {
	if err := h.Limits.Validate(); err != nil {
		return err
	}
	release, err := socketLock(ctx, socket)
	if err != nil {
		return err
	}
	defer release()
	listener, err := (&net.ListenConfig{}).Listen(ctx, "unix", socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(socket)
	if err = os.Chmod(socket, 0600); err != nil {
		return err
	}
	server := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: h.Limits.defaults().Timeout,
		ReadTimeout:       h.Limits.defaults().Timeout,
		WriteTimeout:      h.Limits.defaults().Timeout,
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = server.Close()
		case <-done:
		}
	}()
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
