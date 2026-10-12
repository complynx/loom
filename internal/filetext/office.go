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
	"strconv"
	"strings"
)

func office(ctx context.Context, operation, input string, p parameters, start int) (result, error) {
	reader, err := validatedZIP(input)
	if err != nil {
		return result{}, err
	}
	defer reader.Close()
	switch operation {
	case "office.docx":
		member, memberErr := zipMember(reader, "word/document.xml")
		if memberErr != nil {
			return result{}, memberErr
		}
		defer member.Close()
		return xmlText(contextReader{ctx: ctx, reader: member}, p, start)
	case "office.xlsx":
		return spreadsheet(ctx, reader, p, start)
	case "office.pptx":
		return slides(ctx, reader, p, start)
	case "office.odf":
		member, memberErr := zipMember(reader, "content.xml")
		if memberErr != nil {
			return result{}, memberErr
		}
		defer member.Close()
		return xmlText(contextReader{ctx: ctx, reader: member}, p, start)
	case "office.epub":
		return epub(ctx, reader, p, start)
	default:
		return result{}, errors.New("unsupported office operation")
	}
}

func zipMember(reader *zip.ReadCloser, name string) (io.ReadCloser, error) {
	for _, entry := range reader.File {
		if entry.Name == name {
			member, err := entry.Open()
			if err != nil {
				return nil, fmt.Errorf("open document part: %w", err)
			}
			return member, nil
		}
	}
	return nil, fmt.Errorf("document part %q not found; expected OOXML document", name)
}

type cell struct {
	Reference string   `xml:"r,attr" json:"reference"`
	Type      string   `xml:"t,attr" json:"-"`
	Value     string   `xml:"v"      json:"value"`
	Inline    richText `xml:"is"     json:"-"`
}

type richText struct {
	Text string `xml:"t"`
	Runs []struct {
		Text string `xml:"t"`
	} `xml:"r"`
}

func (r richText) value() string {
	var builder strings.Builder
	builder.WriteString(r.Text)
	for _, run := range r.Runs {
		builder.WriteString(run.Text)
	}
	return builder.String()
}

type sheetRow struct {
	Index int    `xml:"r,attr" json:"index"`
	Cells []cell `xml:"c"      json:"cells"`
}

func spreadsheet(ctx context.Context, reader *zip.ReadCloser, p parameters, start int) (result, error) {
	sheets := worksheetNames(reader)
	if len(sheets) == 0 {
		return result{}, errors.New("XLSX contains no worksheets")
	}
	if p.Sheet == "" {
		end := min(len(sheets), start+limit(p.Limit, maxItems))
		return result{
			data:    map[string]any{"sheets": sheets[min(start, len(sheets)):end], "sheet_count": len(sheets)},
			partial: end < len(sheets), next: end,
		}, nil
	}
	name, err := selectWorksheet(p.Sheet, sheets)
	if err != nil {
		return result{}, err
	}
	shared, err := sharedStrings(ctx, reader)
	if err != nil {
		return result{}, err
	}
	member, err := zipMember(reader, name)
	if err != nil {
		return result{}, err
	}
	defer member.Close()
	return worksheetRows(boundedXML(contextReader{ctx: ctx, reader: member}), name, p, start, shared)
}

func worksheetNames(reader *zip.ReadCloser) []string {
	sheets := make([]string, 0)
	for _, entry := range reader.File {
		if strings.HasPrefix(entry.Name, "xl/worksheets/sheet") && strings.HasSuffix(entry.Name, ".xml") {
			sheets = append(sheets, entry.Name)
		}
	}
	sort.Strings(sheets)
	return sheets
}

func selectWorksheet(name string, sheets []string) (string, error) {
	if !strings.HasPrefix(name, "xl/worksheets/") {
		name = "xl/worksheets/" + name
	}
	if slices.Contains(sheets, name) {
		return name, nil
	}
	return "", errors.New("worksheet not found; use listed worksheet name")
}

func worksheetRows(decoder *xml.Decoder, name string, p parameters, start int, shared []string) (result, error) {
	rows := make([]sheetRow, 0)
	index, total := 0, 0
	for {
		row, err := nextRow(decoder)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return result{}, err
		}
		if index < start {
			index++
			continue
		}
		size, err := resolveCells(&row, shared)
		if err != nil {
			return result{}, err
		}
		if len(rows) >= limit(p.Limit, maxItems) || total+size > maxText {
			return result{data: map[string]any{"sheet": name, keyRows: rows}, partial: true, next: index}, nil
		}
		rows = append(rows, row)
		total += size
		index++
	}
	return result{data: map[string]any{"sheet": name, keyRows: rows}}, nil
}

func nextRow(decoder *xml.Decoder) (sheetRow, error) {
	for {
		token, err := decoder.Token()
		if err != nil {
			return sheetRow{}, err
		}
		element, ok := token.(xml.StartElement)
		if !ok || element.Name.Local != "row" {
			continue
		}
		var row sheetRow
		if err = decoder.DecodeElement(&row, &element); err != nil {
			return row, fmt.Errorf("read worksheet row: %w", err)
		}
		return row, nil
	}
}

func resolveCells(row *sheetRow, shared []string) (int, error) {
	if len(row.Cells) > maxItems {
		return 0, errors.New("worksheet row exceeds 100 cells")
	}
	size := 0
	for i := range row.Cells {
		c := &row.Cells[i]
		if c.Type == "inlineStr" {
			c.Value = c.Inline.value()
		}
		if c.Type == "s" {
			position, err := strconv.Atoi(c.Value)
			if err != nil || position < 0 || position >= len(shared) {
				return 0, errors.New("invalid shared string index")
			}
			c.Value = shared[position]
		}
		size += len(c.Value) + len(c.Reference)
	}
	if size > maxText {
		return 0, errors.New("worksheet row exceeds 32 KiB")
	}
	return size, nil
}

func sharedStrings(ctx context.Context, reader *zip.ReadCloser) ([]string, error) {
	var found *zip.File
	for _, entry := range reader.File {
		if entry.Name == "xl/sharedStrings.xml" {
			found = entry
		}
	}
	if found == nil {
		return []string{}, nil
	}
	member, err := found.Open()
	if err != nil {
		return nil, fmt.Errorf("open shared strings: %w", err)
	}
	defer member.Close()
	decoder := boundedXML(contextReader{ctx: ctx, reader: member})
	values := make([]string, 0)
	total := 0
	for {
		token, tokenErr := decoder.Token()
		if errors.Is(tokenErr, io.EOF) {
			break
		}
		if tokenErr != nil {
			return nil, fmt.Errorf("read shared strings: %w", tokenErr)
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "si" {
			continue
		}
		var item richText
		if err = decoder.DecodeElement(&item, &start); err != nil {
			return nil, fmt.Errorf("decode shared string: %w", err)
		}
		text := item.value()
		total += len(text)
		if total > maxInput || len(values) >= 100000 {
			return nil, errors.New("shared strings exceed bounded document limits")
		}
		values = append(values, text)
	}
	return values, nil
}

func slides(ctx context.Context, reader *zip.ReadCloser, p parameters, start int) (result, error) {
	names := make([]string, 0)
	for _, entry := range reader.File {
		if strings.HasPrefix(entry.Name, "ppt/slides/slide") && strings.HasSuffix(entry.Name, ".xml") {
			names = append(names, entry.Name)
		}
	}
	sort.Slice(names, func(i, j int) bool { return slideNumber(names[i]) < slideNumber(names[j]) })
	if len(names) == 0 {
		return result{}, errors.New("PPTX contains no slides")
	}
	if p.Slide > 0 {
		return selectedSlide(ctx, reader, names, p, start)
	}
	items := make([]map[string]any, 0)
	total := 0
	end := min(len(names), start+limit(p.Limit, maxSlides))
	for index := min(start, len(names)); index < end; index++ {
		member, err := zipMember(reader, names[index])
		if err != nil {
			return result{}, err
		}
		part, err := xmlText(contextReader{ctx: ctx, reader: member}, parameters{Limit: maxText - total}, 0)
		_ = member.Close()
		if err != nil {
			return result{}, err
		}
		textData, ok := part.data.(map[string]any)
		if !ok {
			return result{}, errors.New("invalid slide text result")
		}
		text, ok := textData["text"].(string)
		if !ok {
			return result{}, errors.New("invalid slide text value")
		}
		items = append(
			items,
			map[string]any{"slide": slideNumber(names[index]), keyText: text, "truncated": part.partial},
		)
		total += len(text)
		if total >= maxText || part.partial {
			end = index + 1
			break
		}
	}
	return result{
		data:    map[string]any{keySlides: items, "slide_count": len(names), "cursor_unit": keySlides},
		partial: end < len(names),
		next:    end,
	}, nil
}

func selectedSlide(
	ctx context.Context,
	reader *zip.ReadCloser,
	names []string,
	p parameters,
	start int,
) (result, error) {
	if p.Slide > len(names) {
		return result{}, errors.New("slide not found")
	}
	name := names[p.Slide-1]
	member, err := zipMember(reader, name)
	if err != nil {
		return result{}, err
	}
	defer member.Close()
	part, err := xmlText(contextReader{ctx: ctx, reader: member}, p, start)
	if err != nil {
		return result{}, err
	}
	textData, ok := part.data.(map[string]any)
	if !ok {
		return result{}, errors.New("invalid slide text result")
	}
	part.data = map[string]any{
		keySlides: []map[string]any{
			{"slide": slideNumber(name), keyText: textData[keyText], "truncated": part.partial},
		},
		"slide_count": len(names),
		"cursor_unit": "text_bytes",
	}
	return part, nil
}

func slideNumber(name string) int {
	value := strings.TrimSuffix(strings.TrimPrefix(name, "ppt/slides/slide"), ".xml")
	number, _ := strconv.Atoi(value)
	return number
}
