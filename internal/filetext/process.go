// Package filetext provides bounded document operations inside the file worker.
package filetext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
)

const (
	keySlides          = "slides"
	maxInput           = 20 << 20
	maxText            = 32 << 10
	maxItems           = 100
	maxDepth           = 128
	jsonBudgetDivisor  = 8
	jsonKeyOverhead    = 4
	keyText            = "text"
	keyEncoding        = "encoding"
	keyRows            = "rows"
	keyEntries         = "entries"
	archiveList        = "archive.list"
	archiveExtract     = "archive.extract"
	tarBlockSize       = 512
	utf8BOMSize        = 3
	utf16BOMSize       = 2
	maxSnippet         = 2048
	searchContextBytes = 128
	maxSlides          = 20
	keyFormat          = "format"
	keyOffset          = "offset"
)

// Output identifies a derived file. The parent replaces its path with a scoped handle.
type Output struct {
	Name string `json:"name"`
	MIME string `json:"mime"`
	Path string `json:"path"`
}

type parameters struct {
	Cursor  string `json:"cursor"`
	Limit   int    `json:"limit"`
	Query   string `json:"query"`
	Regex   bool   `json:"regex"`
	Pointer string `json:"pointer"`
	Member  string `json:"member"`
	Sheet   string `json:"sheet"`
	Slide   int    `json:"slide"`
}

type result struct {
	data    any
	outputs []Output
	partial bool
	next    int
}

// Process reads worker-local paths only. Cursors refer to positions in the immutable input.
func Process(
	ctx context.Context,
	operation string,
	params json.RawMessage,
	inputPath, outputDir string,
) (json.RawMessage, []Output, bool, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, false, "", err
	}
	info, err := os.Stat(inputPath)
	if err != nil {
		return nil, nil, false, "", fmt.Errorf("inspect input: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxInput {
		return nil, nil, false, "", errors.New("input must be a regular file of at most 20 MiB")
	}
	var p parameters
	if len(params) > 0 {
		if err = json.Unmarshal(params, &p); err != nil {
			return nil, nil, false, "", fmt.Errorf("invalid parameters: %w", err)
		}
	}
	start, err := cursor(p.Cursor)
	if err != nil {
		return nil, nil, false, "", err
	}
	var r result
	switch operation {
	case "text.read":
		r, err = readText(ctx, inputPath, p, start)
	case "text.search":
		r, err = searchText(ctx, inputPath, p, start)
	case "document.csv":
		r, err = readCSV(ctx, inputPath, p, start)
	case "document.json":
		r, err = readJSON(ctx, inputPath, p)
	case "document.xml":
		r, err = readXML(ctx, inputPath, p, start)
	case "document.html":
		r, err = readHTML(ctx, inputPath, p, start)
	case "archive.list", "archive.extract":
		r, err = archive(ctx, operation, inputPath, outputDir, p, start)
	case "office.docx", "office.xlsx", "office.pptx", "office.odf", "office.epub":
		r, err = office(ctx, operation, inputPath, p, start)
	default:
		err = fmt.Errorf("unsupported document operation %q", operation)
	}
	if err != nil {
		return nil, nil, false, "", err
	}
	data, err := json.Marshal(r.data)
	if err != nil {
		return nil, nil, false, "", fmt.Errorf("encode document result: %w", err)
	}
	next := ""
	if r.partial && r.next >= 0 {
		next = strconv.Itoa(r.next)
	}
	return data, r.outputs, r.partial, next, nil
}

func cursor(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 || n > maxInput*2 {
		return 0, errors.New("cursor must be a nonnegative decimal integer")
	}
	return n, nil
}

func limit(value, maximum int) int {
	if value <= 0 || value > maximum {
		return maximum
	}
	return value
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func openBounded(ctx context.Context, path string) (*os.File, io.Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open input: %w", err)
	}
	return f, contextReader{ctx: ctx, reader: io.LimitReader(f, maxInput+1)}, nil
}
