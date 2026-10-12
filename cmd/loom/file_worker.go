package main

import (
	"context"
	"errors"
	"flag"
	"path/filepath"
	"strings"

	"github.com/complynx/loom/internal/fileimage"
	"github.com/complynx/loom/internal/fileproc"
	"github.com/complynx/loom/internal/filetext"
)

func runFileWorker(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("file-worker", flag.ContinueOnError)
	socket := flags.String("socket", "/run/loom-files/processor.sock", "private file processor socket")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || !filepath.IsAbs(*socket) {
		return errors.New("invalid file processor socket")
	}
	worker := &fileproc.Handler{LocalProcessor: processLocalFile}
	return worker.ListenAndServe(ctx, *socket)
}

func processLocalFile(ctx context.Context, req fileproc.Request, input, dir string) (fileproc.Result, error) {
	if strings.HasPrefix(req.Operation, "image.") {
		data, outputs, partial, cursor, err := fileimage.Process(ctx, req.Operation, req.Params, input, dir)
		result := fileproc.Result{Data: data, Partial: partial, NextCursor: cursor}
		for _, out := range outputs {
			result.Outputs = append(result.Outputs, fileproc.Output{Name: out.Name, MIME: out.MIME, Path: out.Path})
		}
		return result, err
	}
	if strings.HasPrefix(req.Operation, "pdf.") || strings.HasPrefix(req.Operation, "video.") {
		return fileproc.Process(ctx, req, input, dir)
	}
	data, outputs, partial, cursor, err := filetext.Process(ctx, req.Operation, req.Params, input, dir)
	result := fileproc.Result{Data: data, Partial: partial, NextCursor: cursor}
	for _, out := range outputs {
		result.Outputs = append(result.Outputs, fileproc.Output{Name: out.Name, MIME: out.MIME, Path: out.Path})
	}
	return result, err
}
