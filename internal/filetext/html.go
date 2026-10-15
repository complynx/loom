package filetext

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
)

func readHTML(ctx context.Context, input string, p parameters, start int) (result, error) {
	f, raw, err := openBounded(ctx, input)
	if err != nil {
		return result{}, err
	}
	defer f.Close()
	decoded, _, err := textReader(raw)
	if err != nil {
		return result{}, err
	}
	return htmlText(decoded, p, start)
}

// htmlText extracts visible text only; it never fetches links or executes active content.
func htmlText(reader io.Reader, p parameters, start int) (result, error) {
	decoder := xml.NewDecoder(reader)
	decoder.Strict = false
	decoder.AutoClose = xml.HTMLAutoClose
	decoder.Entity = xml.HTMLEntity
	page := htmlPage{page: textPage{start: start, maximum: limit(p.Limit, maxText)}}
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return result{}, fmt.Errorf("parse basic HTML: %w", err)
		}
		if err = page.visit(token); err != nil {
			return result{}, err
		}
		if page.page.full {
			return result{
				data:    map[string]any{keyText: page.page.text.String(), "extraction": "basic_visible_text"},
				partial: true,
				next:    page.page.position,
			}, nil
		}
	}
	return result{data: map[string]any{keyText: page.page.text.String(), "extraction": "basic_visible_text"}}, nil
}

type htmlPage struct {
	page   textPage
	depth  int
	hidden int
}

func (p *htmlPage) visit(token xml.Token) error {
	switch value := token.(type) {
	case xml.StartElement:
		p.depth++
		if p.depth > maxDepth {
			return errors.New("HTML nesting exceeds 128 levels")
		}
		if p.hidden > 0 || hiddenHTML(value.Name.Local) {
			p.hidden++
		}
	case xml.EndElement:
		p.depth--
		if p.hidden > 0 {
			p.hidden--
		}
	case xml.CharData:
		if p.hidden == 0 {
			return p.page.add(strings.TrimSpace(string(value)))
		}
	}
	return nil
}

func hiddenHTML(name string) bool {
	switch strings.ToLower(name) {
	case "script", "style", "head", "template":
		return true
	default:
		return false
	}
}

func epub(ctx context.Context, reader *zip.ReadCloser, p parameters, start int) (result, error) {
	chapters := make([]string, 0)
	for _, entry := range reader.File {
		name := strings.ToLower(entry.Name)
		if strings.HasSuffix(name, ".xhtml") || strings.HasSuffix(name, ".html") || strings.HasSuffix(name, ".htm") {
			chapters = append(chapters, entry.Name)
		}
	}
	sort.Strings(chapters)
	if len(chapters) == 0 {
		return result{}, errors.New("EPUB contains no HTML chapters")
	}
	if p.Member == "" {
		end := min(len(chapters), start+limit(p.Limit, maxItems))
		return result{
			data: map[string]any{
				"chapters":      chapters[min(start, len(chapters)):end],
				"chapter_count": len(chapters),
				"order":         "archive_path",
			},
			partial: end < len(chapters),
			next:    end,
		}, nil
	}
	if !slices.Contains(chapters, p.Member) {
		return result{}, errors.New("EPUB chapter not found; use listed member name")
	}
	member, err := zipMember(reader, p.Member)
	if err != nil {
		return result{}, err
	}
	defer member.Close()
	return htmlText(contextReader{ctx: ctx, reader: member}, p, start)
}
