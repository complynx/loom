package filetext

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"regexp"
	"unicode/utf16"
	"unicode/utf8"
)

type utf16Reader struct {
	reader  io.Reader
	order   binary.ByteOrder
	pending []byte
}

func (r *utf16Reader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(r.pending) == 0 {
		value, err := r.nextRune()
		if err != nil {
			return 0, err
		}
		r.pending = utf8.AppendRune(nil, value)
	}

	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

func (r *utf16Reader) nextRune() (rune, error) {
	var bytes [2]byte
	if _, err := io.ReadFull(r.reader, bytes[:]); err != nil {
		return 0, err
	}
	value := rune(r.order.Uint16(bytes[:]))
	if value >= 0xDC00 && value <= 0xDFFF {
		return 0, errors.New("invalid UTF-16 surrogate")
	}
	if value < 0xD800 || value > 0xDBFF {
		return value, nil
	}
	if _, err := io.ReadFull(r.reader, bytes[:]); err != nil {
		return 0, fmt.Errorf("truncated UTF-16 surrogate: %w", err)
	}
	low := rune(r.order.Uint16(bytes[:]))
	if low < 0xDC00 || low > 0xDFFF {
		return 0, errors.New("invalid UTF-16 surrogate")
	}
	return utf16.DecodeRune(value, low), nil
}

func textReader(reader io.Reader) (io.Reader, string, error) {
	b := bufio.NewReader(reader)
	prefix, err := b.Peek(utf8BOMSize)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, "", err
	}
	if len(prefix) >= 2 && prefix[0] == 0xff && prefix[1] == 0xfe {
		_, _ = b.Discard(utf16BOMSize)
		return &utf16Reader{reader: b, order: binary.LittleEndian}, "utf-16le", nil
	}
	if len(prefix) >= 2 && prefix[0] == 0xfe && prefix[1] == 0xff {
		_, _ = b.Discard(utf16BOMSize)
		return &utf16Reader{reader: b, order: binary.BigEndian}, "utf-16be", nil
	}
	if len(prefix) == 3 && prefix[0] == 0xef && prefix[1] == 0xbb && prefix[2] == 0xbf {
		_, _ = b.Discard(utf8BOMSize)
	}
	return b, "utf-8", nil
}

func validText(data []byte) error {
	if !utf8.Valid(data) || bytes.ContainsRune(data, 0) {
		return errors.New("input is not supported UTF-8 or BOM-marked UTF-16 text")
	}
	return nil
}

func readText(ctx context.Context, path string, p parameters, start int) (result, error) {
	f, raw, err := openBounded(ctx, path)
	if err != nil {
		return result{}, err
	}
	defer f.Close()
	reader, encoding, err := textReader(raw)
	if err != nil {
		return result{}, err
	}
	if _, err = io.CopyN(io.Discard, reader, int64(start)); err != nil && !errors.Is(err, io.EOF) {
		return result{}, err
	}
	data, err := io.ReadAll(io.LimitReader(reader, int64(limit(p.Limit, maxText)+utf8.UTFMax)))
	if err != nil {
		return result{}, fmt.Errorf("read text: %w", err)
	}
	end := min(len(data), limit(p.Limit, maxText))
	for end > 0 && end < len(data) && !utf8.RuneStart(data[end]) {
		end--
	}
	if end == 0 && len(data) > 0 {
		return result{}, errors.New("limit or cursor splits a UTF-8 rune")
	}
	if err = validText(data[:end]); err != nil {
		return result{}, err
	}
	return result{
		data:    map[string]any{keyText: string(data[:end]), keyEncoding: encoding, keyOffset: start},
		partial: end < len(data),
		next:    start + end,
	}, nil
}

type textMatch struct {
	Line      int    `json:"line"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

func boundedLine(reader *bufio.Reader) ([]byte, bool, error) {
	var data []byte
	truncated := false
	for {
		part, more, err := reader.ReadLine()
		if len(data)+len(part) > maxText {
			truncated = true
			part = part[:max(0, maxText-len(data))]
		}
		data = append(data, part...)
		if err != nil || !more {
			if truncated {
				// The retained prefix may stop inside a rune. Interior invalid bytes still fail validation.
				for removed := 0; removed < utf8.UTFMax-1 && len(data) > 0 && !utf8.Valid(data); removed++ {
					data = data[:len(data)-1]
				}
			}
			return data, truncated, err
		}
	}
}

type searchPage struct {
	matches        []textMatch
	total          int
	truncatedLines int
	encoding       string
}

func (p *searchPage) data() map[string]any {
	return map[string]any{
		"matches":                        p.matches,
		keyEncoding:                      p.encoding,
		"truncated_lines":                p.truncatedLines,
		"searched_prefix_bytes_per_line": maxText,
	}
}

func (p *searchPage) add(data []byte, line int, truncated bool, re *regexp.Regexp, maximum int) bool {
	position := re.FindIndex(data)
	if position == nil {
		return true
	}
	begin := max(0, position[0]-searchContextBytes)
	for begin > 0 && !utf8.RuneStart(data[begin]) {
		begin--
	}
	end := min(len(data), begin+maxSnippet)
	for end > begin && !utf8.Valid(data[begin:end]) {
		end--
	}
	snippet := string(data[begin:end])
	if len(p.matches) >= maximum || p.total+len(snippet) > maxText {
		return false
	}
	p.matches = append(
		p.matches,
		textMatch{Line: line, Text: snippet, Truncated: truncated || len(snippet) < len(data)},
	)
	p.total += len(snippet)
	return true
}

func searchText(ctx context.Context, path string, p parameters, start int) (result, error) {
	if p.Query == "" || len(p.Query) > 4096 {
		return result{}, errors.New("query must contain 1 to 4096 bytes")
	}
	pattern := regexp.QuoteMeta(p.Query)
	if p.Regex {
		pattern = p.Query
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return result{}, fmt.Errorf("invalid RE2 expression: %w", err)
	}
	f, raw, err := openBounded(ctx, path)
	if err != nil {
		return result{}, err
	}
	defer f.Close()
	decoded, encoding, err := textReader(raw)
	if err != nil {
		return result{}, err
	}
	return scanSearch(bufio.NewReader(decoded), p, start, re, encoding)
}

func scanSearch(reader *bufio.Reader, p parameters, start int, re *regexp.Regexp, encoding string) (result, error) {
	page := searchPage{matches: make([]textMatch, 0), encoding: encoding}
	for line := 1; ; line++ {
		data, truncated, err := boundedLine(reader)
		if err != nil && !errors.Is(err, io.EOF) {
			return result{}, err
		}
		if errors.Is(err, io.EOF) && len(data) == 0 {
			break
		}
		if err = validText(data); err != nil {
			return result{}, err
		}
		if truncated {
			page.truncatedLines++
		}
		if line > start && !page.add(data, line, truncated, re, limit(p.Limit, maxItems)) {
			return result{data: page.data(), partial: true, next: line - 1}, nil
		}
	}
	return result{data: page.data()}, nil
}
