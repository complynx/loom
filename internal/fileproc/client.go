package fileproc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"time"
)

type Client struct {
	Socket  string
	Timeout time.Duration
}

func (c Client) Process(
	ctx context.Context,
	job Request,
	input io.Reader,
	accept func(Output, io.Reader) error,
) (Result, error) {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	reader, writer := io.Pipe()
	multipartWriter := multipart.NewWriter(writer)
	defer reader.Close()
	stop := context.AfterFunc(ctx, func() {
		_ = reader.CloseWithError(ctx.Err())
		if closer, ok := input.(io.Closer); ok {
			_ = closer.Close()
		}
	})
	defer stop()
	go func() {
		err := writeRequest(multipartWriter, job, input)
		if err == nil {
			err = multipartWriter.Close()
		}
		_ = writer.CloseWithError(err)
	}()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://file-worker/process", reader)
	if err != nil {
		return Result{}, err
	}
	request.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	transport := &http.Transport{DialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(dialCtx, "unix", c.Socket)
	}}
	defer transport.CloseIdleConnections()
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		return Result{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Result{}, errors.New("file processor rejected request")
	}
	return readResponse(response, accept)
}
func readResponse(response *http.Response, accept func(Output, io.Reader) error) (Result, error) {
	media, params, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "multipart/form-data" {
		return Result{}, errors.New("invalid file response")
	}
	parts := multipart.NewReader(response.Body, params["boundary"])
	part, err := parts.NextPart()
	if err != nil {
		return Result{}, err
	}
	if part.FormName() != "result" {
		return Result{}, errors.New("missing result")
	}
	b, err := io.ReadAll(io.LimitReader(part, MaxResultJSONBytes+MaxJobBytes+1))
	if err != nil || len(b) > MaxResultJSONBytes+MaxJobBytes {
		return Result{}, errors.New("result too large")
	}
	var result Result
	if err = decodeOptions(b, &result); err != nil {
		return Result{}, err
	}
	if len(result.Outputs) > MaxFrames {
		return Result{}, errors.New("too many outputs")
	}
	if err = readOutputs(parts, result.Outputs, accept); err != nil {
		return Result{}, err
	}
	return result, nil
}
func readOutputs(parts *multipart.Reader, outputs []Output, accept func(Output, io.Reader) error) error {
	remaining := MaxOutputBytes
	for _, out := range outputs {
		part, err := parts.NextPart()
		if err != nil {
			return err
		}
		if part.FormName() != out.Name {
			return errors.New("unexpected output")
		}
		bounded := &io.LimitedReader{R: part, N: remaining + 1}
		if accept != nil {
			err = accept(out, bounded)
		} else {
			_, err = io.Copy(io.Discard, bounded)
		}
		if err != nil {
			return err
		}
		if _, err = io.Copy(io.Discard, bounded); err != nil {
			return err
		}
		consumed := remaining + 1 - bounded.N
		if consumed > remaining {
			return errors.New("output too large")
		}
		remaining -= consumed
	}
	if _, err := parts.NextPart(); !errors.Is(err, io.EOF) {
		return errors.New("extra output")
	}
	return nil
}

func writeRequest(writer *multipart.Writer, job Request, input io.Reader) error {
	b, err := json.Marshal(job)
	if err != nil {
		return err
	}
	part, err := writer.CreateFormField("job")
	if err != nil {
		return err
	}
	if _, err = part.Write(b); err != nil {
		return err
	}
	part, err = writer.CreateFormFile("input", "input")
	if err != nil {
		return err
	}
	n, err := io.Copy(part, io.LimitReader(input, MaxInputBytes+1))
	if err != nil {
		return err
	}
	if n > MaxInputBytes {
		return errors.New("input too large")
	}
	return nil
}

// Health checks the worker transport without processing a file.
func (c Client) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	transport := &http.Transport{DialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(dialCtx, "unix", c.Socket)
	}}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://file-worker/healthz", nil)
	if err != nil {
		return err
	}
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return errors.New("file worker unhealthy")
	}
	return nil
}
