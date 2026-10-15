package filetext

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

func readCSV(ctx context.Context, path string, p parameters, start int) (result, error) {
	f, raw, err := openBounded(ctx, path)
	if err != nil {
		return result{}, err
	}
	defer f.Close()
	decoded, encoding, err := textReader(raw)
	if err != nil {
		return result{}, err
	}
	reader := csv.NewReader(decoded)
	reader.FieldsPerRecord = -1
	rows := make([][]string, 0)
	index, total := 0, 0
	for {
		row, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return result{}, fmt.Errorf("read CSV row %d: %w", index+1, readErr)
		}
		if index < start {
			index++
			continue
		}
		size := 0
		for _, cell := range row {
			if err = validText([]byte(cell)); err != nil {
				return result{}, err
			}
			size += len(cell)
		}
		if len(row) > maxItems || size > maxText {
			return result{}, fmt.Errorf("CSV row %d exceeds 100 columns or 32 KiB; use text.read", index+1)
		}
		if len(rows) >= limit(p.Limit, maxItems) || total+size > maxText {
			return result{
				data:    map[string]any{keyRows: rows, keyEncoding: encoding, keyOffset: start},
				partial: true,
				next:    index,
			}, nil
		}
		rows = append(rows, row)
		total += size
		index++
	}
	return result{data: map[string]any{keyRows: rows, keyEncoding: encoding, keyOffset: start}}, nil
}

func readXML(ctx context.Context, path string, p parameters, start int) (result, error) {
	f, raw, err := openBounded(ctx, path)
	if err != nil {
		return result{}, err
	}
	defer f.Close()
	decoded, _, err := textReader(raw)
	if err != nil {
		return result{}, err
	}
	return xmlText(decoded, p, start)
}

func xmlText(reader io.Reader, p parameters, start int) (result, error) {
	decoder := xml.NewDecoder(reader)
	page := textPage{start: start, maximum: limit(p.Limit, maxText)}
	depth := 0
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return result{}, fmt.Errorf("invalid XML: %w", err)
		}
		if err = xmlDepth(token, &depth); err != nil {
			return result{}, err
		}
		value, ok := token.(xml.CharData)
		if !ok {
			continue
		}
		if err = page.add(strings.TrimSpace(string(value))); err != nil {
			return result{}, err
		}
		if page.full {
			return page.result(), nil
		}
	}
	return page.result(), nil
}

type textPage struct {
	text     strings.Builder
	start    int
	position int
	maximum  int
	full     bool
}

func (p *textPage) add(value string) error {
	if value == "" {
		return nil
	}
	value += "\n"
	if p.position+len(value) <= p.start {
		p.position += len(value)
		return nil
	}
	skip := max(0, p.start-p.position)
	if skip > 0 && skip < len(value) && !utf8.RuneStart(value[skip]) {
		return errors.New("cursor splits a UTF-8 rune")
	}
	value = value[skip:]
	p.position += skip
	remaining := p.maximum - p.text.Len()
	end := min(len(value), remaining)
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	if end == 0 && remaining > 0 {
		return errors.New("limit is too small for a UTF-8 rune")
	}
	p.text.WriteString(value[:end])
	p.position += end
	p.full = end < len(value)
	return nil
}

func (p *textPage) result() result {
	return result{data: map[string]any{keyText: p.text.String()}, partial: p.full, next: p.position}
}

func xmlDepth(token xml.Token, depth *int) error {
	switch token.(type) {
	case xml.StartElement:
		*depth++
		if *depth > maxDepth {
			return errors.New("XML nesting exceeds 128 levels")
		}
	case xml.EndElement:
		*depth--
	case xml.Directive:
		return errors.New("XML directives and DTDs are not supported")
	}
	return nil
}

type checkedXMLTokens struct {
	decoder *xml.Decoder
	depth   int
}

func (r *checkedXMLTokens) Token() (xml.Token, error) {
	token, err := r.decoder.Token()
	if err != nil {
		return nil, err
	}
	if err = xmlDepth(token, &r.depth); err != nil {
		return nil, err
	}
	return token, nil
}

func boundedXML(reader io.Reader) *xml.Decoder {
	return xml.NewTokenDecoder(&checkedXMLTokens{decoder: xml.NewDecoder(reader)})
}

func readJSON(ctx context.Context, path string, p parameters) (result, error) {
	if p.Pointer != "" && !strings.HasPrefix(p.Pointer, "/") {
		return result{}, errors.New("JSON pointer must be empty or begin with /")
	}
	f, raw, err := openBounded(ctx, path)
	if err != nil {
		return result{}, err
	}
	defer f.Close()
	decoded, _, err := textReader(raw)
	if err != nil {
		return result{}, err
	}
	decoder := json.NewDecoder(decoded)
	decoder.UseNumber()
	parts := []string{}
	if p.Pointer != "" {
		for part := range strings.SplitSeq(p.Pointer[1:], "/") {
			parts = append(parts, strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~"))
		}
	}
	budget := maxText / jsonBudgetDivisor
	value, found, partial, err := jsonValue(decoder, parts, 0, &budget, true)
	if err != nil {
		return result{}, err
	}
	if !found {
		return result{}, errors.New("JSON pointer not found")
	}
	if _, err = decoder.Token(); !errors.Is(err, io.EOF) {
		return result{}, errors.New("JSON contains trailing data")
	}
	return result{
		data:    map[string]any{"value": value, "pointer": p.Pointer, "summary": partial},
		partial: partial,
		next:    -1,
	}, nil
}

type jsonParser struct {
	decoder *json.Decoder
	budget  int
	partial bool
}

func jsonValue(decoder *json.Decoder, path []string, depth int, budget *int, collect bool) (any, bool, bool, error) {
	parser := jsonParser{decoder: decoder, budget: *budget}
	value, found, err := parser.value(path, depth, collect)
	*budget = parser.budget
	return value, found, parser.partial, err
}

func (p *jsonParser) value(path []string, depth int, collect bool) (any, bool, error) {
	if depth >= maxDepth {
		return nil, false, errors.New("JSON nesting exceeds 128 levels")
	}
	token, err := p.decoder.Token()
	if err != nil {
		return nil, false, fmt.Errorf("invalid JSON: %w", err)
	}
	delim, container := token.(json.Delim)
	if !container {
		return p.scalar(token, path, collect)
	}
	return p.container(delim, path, depth, collect)
}

func (p *jsonParser) scalar(token any, path []string, collect bool) (any, bool, error) {
	if !collect || len(path) > 0 {
		return nil, false, nil
	}
	if text, ok := token.(string); ok {
		end := min(len(text), max(0, p.budget))
		for end > 0 && !utf8.ValidString(text[:end]) {
			end--
		}
		if end < len(text) {
			p.partial = true
		}
		token = text[:end]
	}
	encoded, _ := json.Marshal(token)
	p.budget -= len(encoded)
	return token, true, nil
}

func (p *jsonParser) container(delim json.Delim, path []string, depth int, collect bool) (any, bool, error) {
	object := map[string]any{}
	array := make([]any, 0)
	var selected any
	found := false
	for index := 0; p.decoder.More(); index++ {
		key, err := p.key(delim, index)
		if err != nil {
			return nil, false, err
		}
		childPath, wanted := p.selectChild(path, key, index, collect)
		child, childFound, err := p.value(childPath, depth+1, wanted)
		if err != nil {
			return nil, false, err
		}
		if len(path) > 0 && wanted && childFound {
			selected = child
			found = true
		}
		if len(path) == 0 && collect {
			if wanted && childFound {
				addJSONChild(delim, object, &array, key, child)
			} else {
				p.partial = true
			}
		}
	}
	if _, err := p.decoder.Token(); err != nil {
		return nil, false, err
	}
	if len(path) > 0 {
		return selected, found, nil
	}
	if !collect {
		return nil, false, nil
	}
	if delim == '{' {
		return object, true, nil
	}
	return array, true, nil
}

func (p *jsonParser) key(delim json.Delim, index int) (string, error) {
	if delim != '{' {
		return strconv.Itoa(index), nil
	}
	token, err := p.decoder.Token()
	if err != nil {
		return "", err
	}
	key, ok := token.(string)
	if !ok {
		return "", errors.New("JSON object key is not a string")
	}
	return key, nil
}

func (p *jsonParser) selectChild(path []string, key string, index int, collect bool) ([]string, bool) {
	if len(path) > 0 {
		return path[1:], collect && key == path[0]
	}
	wanted := collect && index < maxItems && len(key) <= p.budget
	if wanted {
		p.budget -= len(key) + jsonKeyOverhead
	}
	return path, wanted && p.budget > 0
}

func addJSONChild(delim json.Delim, object map[string]any, array *[]any, key string, value any) {
	if delim == '{' {
		object[key] = value
		return
	}
	*array = append(*array, value)
}
