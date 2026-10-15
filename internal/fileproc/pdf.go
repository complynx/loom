package fileproc

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

type PDFOptions struct {
	Page     int    `json:"page,omitempty"`
	EndPage  int    `json:"end_page,omitempty"`
	Query    string `json:"query,omitempty"`
	Regex    bool   `json:"regex,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	X        int    `json:"x,omitempty"`
	Y        int    `json:"y,omitempty"`
	MaxChars int    `json:"max_chars,omitempty"`
	Offset   int64  `json:"offset,omitempty"`
}

func processPDF(ctx context.Context, req Request, input, dir string) (Result, error) {
	var p PDFOptions
	if err := decodeOptions(req.Params, &p); err != nil {
		return Result{}, err
	}
	if p.Page == 0 {
		p.Page = 1
	}
	if p.EndPage == 0 {
		p.EndPage = p.Page
	}
	if p.Width == 0 {
		p.Width = 1280
	}
	if p.MaxChars == 0 {
		p.MaxChars = 8192
	}
	if p.Offset < 0 || p.Page < 1 || p.EndPage < p.Page || p.EndPage-p.Page >= 24 || p.Width < 1 || p.Width > 2048 ||
		p.Height < 0 ||
		p.Height > 2048 ||
		p.X < 0 ||
		p.Y < 0 ||
		p.MaxChars < 1 ||
		p.MaxChars > 8192 {
		return Result{}, errors.New("invalid PDF options")
	}
	switch req.Operation {
	case "pdf.info":
		return pdfInfo(ctx, p, input)
	case "pdf.images":
		return pdfImages(ctx, p, input, dir)
	case "pdf.render":
		return pdfRender(ctx, p, input, dir)
	default:
		return pdfText(ctx, req, p, input, dir)
	}
}
func pdfInfo(ctx context.Context, p PDFOptions, input string) (Result, error) {
	b, err := command(ctx, "pdfinfo", "-f", strconv.Itoa(p.Page), "-l", strconv.Itoa(p.EndPage), input)
	if err != nil {
		return Result{}, err
	}
	return dataResult(map[string]any{"info": string(b)})
}
func pdfImages(ctx context.Context, p PDFOptions, input, dir string) (Result, error) {
	prefix := filepath.Join(dir, "embedded")
	if _, err := command(
		ctx,
		"pdfimages",
		"-png",
		"-f",
		strconv.Itoa(p.Page),
		"-l",
		strconv.Itoa(p.EndPage),
		input,
		prefix,
	); err != nil {
		return Result{}, err
	}
	files, err := filepath.Glob(prefix + "-*.png")
	if err != nil {
		return Result{}, err
	}
	start := int(min(p.Offset, int64(len(files))))
	end := min(start+MaxFrames, len(files))
	result, err := dataResult(map[string]any{"images": len(files), "first_image": start})
	if err != nil {
		return Result{}, err
	}
	for _, path := range files[start:end] {
		result.Outputs = append(result.Outputs, Output{Name: filepath.Base(path), MIME: "image/png", Path: path})
	}
	result.Partial = end < len(files)
	if result.Partial {
		result.NextCursor = strconv.Itoa(end)
	}
	return result, nil
}
func pdfRender(ctx context.Context, p PDFOptions, input, dir string) (Result, error) {
	prefix := filepath.Join(dir, "page")
	args := []string{
		"-png",
		"-f",
		strconv.Itoa(p.Page),
		"-l",
		strconv.Itoa(p.EndPage),
		"-scale-to",
		strconv.Itoa(p.Width),
	}
	if p.Height > 0 {
		args = append(
			args,
			"-x",
			strconv.Itoa(p.X),
			"-y",
			strconv.Itoa(p.Y),
			"-W",
			strconv.Itoa(p.Width),
			"-H",
			strconv.Itoa(p.Height),
		)
	}
	args = append(args, input, prefix)
	if _, err := command(ctx, "pdftoppm", args...); err != nil {
		return Result{}, err
	}
	files, err := filepath.Glob(prefix + "-*.png")
	if err != nil {
		return Result{}, err
	}
	if len(files) == 0 {
		return Result{}, errors.New("no PDF pages rendered")
	}
	result, err := dataResult(map[string]any{"first_page": p.Page, "last_page": p.EndPage})
	if err != nil {
		return Result{}, err
	}
	for _, path := range files {
		result.Outputs = append(result.Outputs, Output{Name: filepath.Base(path), MIME: "image/png", Path: path})
	}
	return result, nil
}

func pdfText(
	ctx context.Context,
	req Request,
	p PDFOptions,
	input, dir string,
) (Result, error) { // A file avoids retaining pdftotext's unbounded stdout. Only a bounded prefix is read.
	path := filepath.Join(dir, "text.txt")
	if _, err := command(
		ctx,
		"pdftotext",
		"-layout",
		"-f",
		strconv.Itoa(p.Page),
		"-l",
		strconv.Itoa(p.EndPage),
		input,
		path,
	); err != nil {
		return Result{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return Result{}, err
	}
	defer f.Close()
	if _, err = f.Seek(p.Offset, io.SeekStart); err != nil {
		return Result{}, err
	}
	if req.Operation == "pdf.search" {
		return pdfSearchFile(ctx, p, f)
	}
	b := make([]byte, p.MaxChars+1)
	n, err := io.ReadFull(f, b)
	if err != nil && n == 0 && !errors.Is(err, io.EOF) {
		return Result{}, err
	}
	if n > 0 && !utf8.RuneStart(b[0]) {
		return Result{}, errors.New("PDF text offset must start at a UTF-8 boundary")
	}
	partial := n > p.MaxChars
	if partial {
		b = b[:p.MaxChars]
	} else {
		b = b[:n]
	}
	for len(b) > 0 && !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	if n > 0 && len(b) == 0 {
		return Result{}, errors.New("PDF text limit cannot fit the next UTF-8 character")
	}
	text := string(b)
	var value any = map[string]any{"page": p.Page, "end_page": p.EndPage, "text": text}
	result, err := dataResult(value)
	result.Partial = partial
	if partial {
		result.NextCursor = strconv.FormatInt(p.Offset+int64(len(b)), 10)
	}
	return result, err
}

// Search cursors advance by complete lines, never by arbitrary byte chunks.
// Oversized lines are skipped with an explicit count rather than searched partially.
func pdfSearchFile(ctx context.Context, p PDFOptions, f *os.File) (Result, error) {
	pattern, err := pdfSearchPattern(p)
	if err != nil {
		return Result{}, err
	}
	info, err := f.Stat()
	if err != nil {
		return Result{}, err
	}
	reader := bufio.NewReaderSize(f, MaxTextBytes)
	first, err := reader.Peek(1)
	if err != nil && !errors.Is(err, io.EOF) {
		return Result{}, err
	}
	if len(first) > 0 && !utf8.RuneStart(first[0]) {
		return Result{}, errors.New("PDF text offset must start at a UTF-8 boundary")
	}
	matches := []map[string]any{}
	consumed := int64(0)
	skipped := 0
	excerptBytes := 0
	for line := 1; consumed < int64(p.MaxChars) && len(matches) < MaxSearchMatches; line++ {
		text, count, truncated, readErr := pdfSearchLine(ctx, reader)
		if readErr != nil {
			return Result{}, readErr
		}
		if count == 0 {
			break
		}
		consumed += count
		if truncated {
			skipped++
			continue
		}
		if !pattern.Match(text) {
			continue
		}
		excerpt := pdfSearchExcerpt(text)
		matches = append(matches, map[string]any{"line": line, "text": excerpt})
		excerptBytes += len(excerpt)
		if excerptBytes >= MaxStderrBytes {
			break
		}
	}
	result, err := dataResult(
		map[string]any{
			"page":               p.Page,
			"matches":            matches,
			"skipped_long_lines": skipped,
			"line_limit_bytes":   MaxTextBytes,
		},
	)
	result.Partial = p.Offset+consumed < info.Size() || skipped > 0
	if result.Partial {
		result.NextCursor = strconv.FormatInt(p.Offset+consumed, 10)
	}
	return result, err
}
func pdfSearchPattern(p PDFOptions) (*regexp.Regexp, error) {
	if p.Query == "" || len(p.Query) > 4096 {
		return nil, errors.New("PDF search query is required and must fit 4096 bytes")
	}
	pattern := regexp.QuoteMeta(p.Query)
	if p.Regex {
		pattern = p.Query
	}
	return regexp.Compile(pattern)
}
func pdfSearchLine(ctx context.Context, reader *bufio.Reader) ([]byte, int64, bool, error) {
	var text []byte
	var count int64
	truncated := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, count, truncated, err
		}
		chunk, err := reader.ReadSlice('\n')
		count += int64(len(chunk))
		if !truncated {
			if len(text)+len(chunk) > MaxTextBytes || errors.Is(err, bufio.ErrBufferFull) {
				truncated = true
				text = nil
			} else {
				text = append(text, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, count, truncated, err
		}
		return text, count, truncated, nil
	}
}
func pdfSearchExcerpt(text []byte) string {
	text = []byte(strings.TrimSuffix(string(text), "\n"))
	if len(text) > MaxSearchLine {
		text = text[:MaxSearchLine]
	}
	for len(text) > 0 && !utf8.Valid(text) {
		text = text[:len(text)-1]
	}
	return string(text)
}
