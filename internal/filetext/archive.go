package filetext

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	maxEntries          = 1000
	maxCompressionRatio = 200
)

type archiveEntry struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Directory bool   `json:"directory"`
}

func safeMember(name string) bool {
	if name == "" || !utf8.ValidString(name) || strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "/") {
		return false
	}
	return len(name) <= 256 && !slices.Contains(strings.Split(name, "/"), "..")
}

func validatedZIP(input string) (*zip.ReadCloser, error) {
	reader, err := zip.OpenReader(input)
	if err != nil {
		return nil, fmt.Errorf("open ZIP: %w", err)
	}
	if len(reader.File) > maxEntries {
		_ = reader.Close()
		return nil, errors.New("archive exceeds 1000 entries")
	}
	var total uint64
	seen := make(map[string]bool)
	for _, entry := range reader.File {
		total += entry.UncompressedSize64
		invalid := seen[entry.Name] || !safeMember(entry.Name) ||
			(!entry.Mode().IsRegular() && !entry.Mode().IsDir()) ||
			entry.Flags&1 != 0
		seen[entry.Name] = true
		invalid = invalid || entry.UncompressedSize64 > maxInput || entry.CompressedSize64 > maxInput ||
			total > maxInput
		invalid = invalid || entry.UncompressedSize64 > max(1, entry.CompressedSize64)*maxCompressionRatio
		if invalid {
			_ = reader.Close()
			return nil, errors.New("archive contains unsafe, encrypted, oversized, or excessive-ratio entries")
		}
	}
	return reader, nil
}

func archive(ctx context.Context, operation, input, outputDir string, p parameters, start int) (result, error) {
	f, err := os.Open(input)
	if err != nil {
		return result{}, fmt.Errorf("open archive: %w", err)
	}
	var magic [4]byte
	_, readErr := io.ReadFull(f, magic[:])
	_ = f.Close()
	if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return result{}, readErr
	}
	if magic[0] == 'P' && magic[1] == 'K' {
		return zipOperation(ctx, operation, input, outputDir, p, start)
	}
	return tarOperation(ctx, operation, input, outputDir, p, start, magic[0] == 0x1f && magic[1] == 0x8b)
}

func zipOperation(ctx context.Context, operation, input, outputDir string, p parameters, start int) (result, error) {
	reader, err := validatedZIP(input)
	if err != nil {
		return result{}, err
	}
	defer reader.Close()
	if operation == archiveList {
		entries := make([]archiveEntry, 0)
		end := min(len(reader.File), start+limit(p.Limit, maxItems))
		for index := min(start, len(reader.File)); index < end; index++ {
			if err = ctx.Err(); err != nil {
				return result{}, err
			}
			entry := reader.File[index]
			entries = append(
				entries,
				archiveEntry{Name: entry.Name, Size: entry.FileInfo().Size(), Directory: entry.FileInfo().IsDir()},
			)
		}
		return result{
			data:    map[string]any{keyFormat: "zip", keyEntries: entries, "entry_count": len(reader.File)},
			partial: end < len(reader.File),
			next:    end,
		}, nil
	}
	if !safeMember(p.Member) {
		return result{}, errors.New("member must be a safe archive-relative path")
	}
	for _, entry := range reader.File {
		if entry.Name != p.Member {
			continue
		}
		if !entry.Mode().IsRegular() {
			return result{}, errors.New("member is not a regular file")
		}
		member, openErr := entry.Open()
		if openErr != nil {
			return result{}, fmt.Errorf("open archive member: %w", openErr)
		}
		defer member.Close()
		return extracted(ctx, member, outputDir, p.Member, entry.FileInfo().Size())
	}
	return result{}, errors.New("archive member not found")
}

func extracted(ctx context.Context, reader io.Reader, outputDir, name string, expected int64) (result, error) {
	file, err := os.CreateTemp(outputDir, "member-*")
	if err != nil {
		return result{}, fmt.Errorf("create derived file: %w", err)
	}
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(file.Name())
		}
	}()
	n, err := io.Copy(file, io.LimitReader(contextReader{ctx: ctx, reader: reader}, maxInput+1))
	if err != nil {
		return result{}, fmt.Errorf("extract member: %w", err)
	}
	if n > maxInput || expected >= 0 && n != expected {
		return result{}, errors.New("archive member size exceeds limit or disagrees with header")
	}
	if err = file.Close(); err != nil {
		return result{}, fmt.Errorf("close derived file: %w", err)
	}
	keep = true
	return result{
		data:    map[string]any{"name": name, "bytes": n},
		outputs: []Output{{Name: path.Base(name), MIME: "application/octet-stream", Path: file.Name()}},
	}, nil
}

func tarOperation(
	ctx context.Context,
	operation, input, outputDir string,
	p parameters,
	start int,
	compressed bool,
) (result, error) {
	f, raw, err := openBounded(ctx, input)
	if err != nil {
		return result{}, err
	}
	defer f.Close()
	if compressed {
		gz, gzErr := gzip.NewReader(raw)
		if gzErr != nil {
			return result{}, fmt.Errorf("open gzip: %w", gzErr)
		}
		defer gz.Close()
		info, statErr := f.Stat()
		if statErr != nil {
			return result{}, fmt.Errorf("inspect compressed archive: %w", statErr)
		}
		expandedLimit := min(int64(maxInput), info.Size()*maxCompressionRatio)
		raw = contextReader{ctx: ctx, reader: &expansionReader{reader: gz, remaining: expandedLimit}}
		if !strings.HasSuffix(strings.ToLower(input), ".tar.gz") && !strings.HasSuffix(strings.ToLower(input), ".tgz") {
			// Worker input names are opaque, so detect TAR by its first block rather than extension.
			return gzipOrTar(ctx, operation, raw, outputDir, p, start)
		}
	}
	return walkTar(ctx, operation, raw, outputDir, p, start)
}

type expansionReader struct {
	reader    io.Reader
	remaining int64
}

func (r *expansionReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.remaining == 0 {
		var probe [1]byte
		n, err := r.reader.Read(probe[:])
		if n > 0 {
			return 0, errors.New("gzip expansion exceeds size or compression-ratio limit")
		}
		return 0, err
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	return n, err
}

func gzipOrTar(
	ctx context.Context,
	operation string,
	raw io.Reader,
	outputDir string,
	p parameters,
	start int,
) (result, error) {
	prefix := make([]byte, tarBlockSize)
	n, err := io.ReadFull(raw, prefix)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return result{}, err
	}
	reader := io.MultiReader(strings.NewReader(string(prefix[:n])), raw)
	if n == 512 && string(prefix[257:262]) == "ustar" {
		return walkTar(ctx, operation, reader, outputDir, p, start)
	}
	if operation == archiveList {
		return result{
			data: map[string]any{keyFormat: "gzip", keyEntries: []archiveEntry{{Name: "content", Size: -1}}},
		}, nil
	}
	if p.Member != "content" {
		return result{}, errors.New("gzip member is named content")
	}
	return extracted(ctx, reader, outputDir, "content", -1)
}

type tarPage struct {
	entries  []archiveEntry
	selected result
	found    bool
	index    int
	total    int64
}

func walkTar(
	ctx context.Context,
	operation string,
	raw io.Reader,
	outputDir string,
	p parameters,
	start int,
) (result, error) {
	reader := tar.NewReader(raw)
	page := tarPage{entries: make([]archiveEntry, 0)}
	keep := false
	defer func() {
		if !keep {
			for _, output := range page.selected.outputs {
				_ = os.Remove(output.Path)
			}
		}
	}()
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return result{}, fmt.Errorf("read TAR: %w", err)
		}
		if err = ctx.Err(); err != nil {
			return result{}, err
		}
		page.total += header.Size
		if err = validateTarHeader(header, page.index, page.total); err != nil {
			return result{}, err
		}
		if err = page.visit(ctx, reader, operation, outputDir, p, start, header); err != nil {
			return result{}, err
		}
		page.index++
	}
	if operation == archiveExtract {
		if !page.found {
			return result{}, errors.New("archive member not found")
		}
		keep = true
		return page.selected, nil
	}
	end := min(page.index, start+len(page.entries))
	return result{
		data:    map[string]any{keyFormat: "tar", keyEntries: page.entries, "entry_count": page.index},
		partial: end < page.index,
		next:    end,
	}, nil
}

func (p *tarPage) visit(
	ctx context.Context,
	reader io.Reader,
	operation, outputDir string,
	params parameters,
	start int,
	header *tar.Header,
) error {
	if operation == archiveList && p.index >= start && len(p.entries) < limit(params.Limit, maxItems) {
		p.entries = append(
			p.entries,
			archiveEntry{Name: header.Name, Size: header.Size, Directory: header.Typeflag == tar.TypeDir},
		)
	}
	if operation != archiveExtract || header.Name != params.Member {
		return nil
	}
	if p.found || header.Typeflag == tar.TypeDir {
		return errors.New("duplicate or nonregular archive member")
	}
	var err error
	p.selected, err = extracted(ctx, reader, outputDir, params.Member, header.Size)
	p.found = err == nil
	return err
}

func validateTarHeader(header *tar.Header, index int, total int64) error {
	if index >= maxEntries || !safeMember(header.Name) || header.Size < 0 || total > maxInput {
		return errors.New("TAR contains unsafe or oversized entries")
	}
	if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir {
		return errors.New("TAR contains unsupported entries")
	}
	return nil
}
