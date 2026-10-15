package filetext_test

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/complynx/loom/internal/filetext"
)

func fixture(t *testing.T, content string) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(name, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return name
}

func run(t *testing.T, operation, path, params string) (string, bool, string) {
	t.Helper()
	data, _, partial, next, err := filetext.Process(t.Context(), operation, json.RawMessage(params), path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return string(data), partial, next
}

func zipFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "input.zip")
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	for name, content := range files {
		member, createErr := w.Create(name)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, err = member.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestTextPagingAndSearch(t *testing.T) {
	t.Parallel()
	path := fixture(t, "Привет\nneedle first\nneedle second\n")
	data, partial, next := run(t, "text.read", path, `{"limit":12}`)
	if !strings.Contains(data, "Привет") || !partial || next != "12" {
		t.Fatalf("unexpected page: %s %v %s", data, partial, next)
	}
	data, partial, next = run(t, "text.search", path, `{"query":"needle","limit":1}`)
	if !strings.Contains(data, "needle first") || !partial || next != "2" {
		t.Fatalf("unexpected search: %s %v %s", data, partial, next)
	}
	data, partial, _ = run(t, "text.search", path, `{"query":"needle","cursor":"2"}`)
	if !strings.Contains(data, "needle second") || partial {
		t.Fatalf("unexpected next page: %s", data)
	}
	utf16 := fixture(t, string([]byte{0xff, 0xfe, 'o', 0, 'k', 0}))
	data, _, _ = run(t, "text.read", utf16, `{}`)
	if !strings.Contains(data, `"text":"ok"`) {
		t.Fatalf("UTF16 decode: %s", data)
	}
}

func TestSearchLongUTF8LineRetainsValidPrefix(t *testing.T) {
	t.Parallel()
	data, partial, _ := run(t, "text.search", fixture(t, strings.Repeat("a", 32767)+"€ tail\n"), `{"query":"a"}`)
	var page struct {
		TruncatedLines int `json:"truncated_lines"`
		Matches        []struct {
			Truncated bool `json:"truncated"`
		} `json:"matches"`
	}
	if err := json.Unmarshal([]byte(data), &page); err != nil {
		t.Fatal(err)
	}
	if partial || page.TruncatedLines != 1 || len(page.Matches) != 1 || !page.Matches[0].Truncated {
		t.Fatalf("long line search: %s", data)
	}
}

func TestSpreadsheetPreservesRichInlineStrings(t *testing.T) {
	t.Parallel()
	input := zipFixture(t, map[string]string{
		"xl/worksheets/sheet1.xml": `<worksheet><sheetData><row r="1"><c r="A1" t="inlineStr"><is><r><t>Hello </t></r><r><t>world</t></r></is></c><c r="B1" t="inlineStr"><is><t>plain</t></is></c><c r="C1" t="s"><v>0</v></c></row></sheetData></worksheet>`,
		"xl/sharedStrings.xml":     `<sst><si><r><t>Shared </t></r><r><t>value</t></r></si></sst>`,
	})
	data, _, _ := run(t, "office.xlsx", input, `{"sheet":"sheet1.xml"}`)
	for _, value := range []string{"Hello world", "plain", "Shared value"} {
		if !strings.Contains(data, value) {
			t.Fatalf("cell text missing %q: %s", value, data)
		}
	}
}

func TestLongSlideTextHasCompleteByteContinuation(t *testing.T) {
	t.Parallel()
	var text strings.Builder
	for index := range 5000 {
		text.WriteString("Привет " + strconv.Itoa(index) + " ")
	}
	input := zipFixture(
		t,
		map[string]string{"ppt/slides/slide1.xml": "<slide><p><t>" + text.String() + "</t></p></slide>"},
	)
	data, _, _ := run(t, "office.pptx", input, `{}`)
	if !strings.Contains(data, `"truncated":true`) {
		t.Fatal("overview must report truncated slide")
	}
	combined := readAllPages(t, "office.pptx", input)
	if combined != strings.TrimSpace(text.String())+"\n" {
		t.Fatal("explicit slide continuation lost or duplicated text")
	}
}

func TestDocumentViews(t *testing.T) {
	t.Parallel()
	data, partial, next := run(
		t,
		"document.csv",
		fixture(t, "name,note\nAlice,\"hello, world\"\nBob,ok\n"),
		`{"cursor":"1","limit":1}`,
	)
	if !partial || next != "2" || !strings.Contains(data, "hello, world") {
		t.Fatalf("CSV: %s %v %s", data, partial, next)
	}
	data, _, _ = run(
		t,
		"document.json",
		fixture(t, `{"a/b":{"~key":[1,{"title":"hello"}]},"other":true}`),
		`{"pointer":"/a~1b/~0key/1/title"}`,
	)
	if !strings.Contains(data, `"value":"hello"`) {
		t.Fatalf("JSON pointer: %s", data)
	}
	data, partial, next = run(t, "document.xml", fixture(t, `<root><p>hello</p><p>world</p></root>`), `{"limit":6}`)
	if !partial || next != "6" || !strings.Contains(data, "hello") {
		t.Fatalf("XML: %s %v %s", data, partial, next)
	}
}

func TestArchiveAndOffice(t *testing.T) {
	t.Parallel()
	archive := zipFixture(t, map[string]string{"folder/a.txt": "hello"})
	data, outputs, _, _, err := filetext.Process(
		t.Context(),
		"archive.extract",
		json.RawMessage(`{"member":"folder/a.txt"}`),
		archive,
		t.TempDir(),
	)
	if err != nil || len(outputs) != 1 {
		t.Fatalf("extract: %s %v", data, err)
	}
	content, err := os.ReadFile(outputs[0].Path)
	if err != nil || string(content) != "hello" {
		t.Fatalf("extracted bytes: %q %v", content, err)
	}
	docx := zipFixture(t, map[string]string{"word/document.xml": `<document><p><t>Hello document</t></p></document>`})
	text, _, _ := run(t, "office.docx", docx, `{}`)
	if !strings.Contains(text, "Hello document") {
		t.Fatal(text)
	}
	xlsx := zipFixture(
		t,
		map[string]string{
			"xl/worksheets/sheet1.xml": `<worksheet><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="inlineStr"><is><t>World</t></is></c></row></sheetData></worksheet>`,
			"xl/sharedStrings.xml":     `<sst><si><t>Hello</t></si></sst>`,
		},
	)
	text, _, _ = run(t, "office.xlsx", xlsx, `{"sheet":"sheet1.xml"}`)
	if !strings.Contains(text, "Hello") || !strings.Contains(text, "World") {
		t.Fatal(text)
	}
	pptx := zipFixture(t, map[string]string{"ppt/slides/slide1.xml": `<slide><p><t>Slide one</t></p></slide>`})
	text, _, _ = run(t, "office.pptx", pptx, `{}`)
	if !strings.Contains(text, "Slide one") {
		t.Fatal(text)
	}
}

func TestHTMLAndOpenDocumentBooks(t *testing.T) {
	t.Parallel()
	text, partial, next := run(
		t,
		"document.html",
		fixture(
			t,
			`<!doctype html><html><head><title>hidden</title></head><body><script>secret</script><style>hidden</style><p>Hello &amp; world</p><br><p>Again</p></body></html>`,
		),
		`{"limit":14}`,
	)
	if !strings.Contains(text, `Hello \u0026 world`) || strings.Contains(text, "secret") ||
		strings.Contains(text, "hidden") ||
		!partial ||
		next != "14" {
		t.Fatalf("HTML view: %s %v %s", text, partial, next)
	}
	odf := zipFixture(
		t,
		map[string]string{
			"content.xml": `<document><body><text><p>ODF paragraph</p></text><table><row><cell><p>Cell text</p></cell></row></table></body></document>`,
		},
	)
	text, _, _ = run(t, "office.odf", odf, `{}`)
	if !strings.Contains(text, "ODF paragraph") || !strings.Contains(text, "Cell text") {
		t.Fatal(text)
	}
	epub := zipFixture(t, map[string]string{"book/chapter1.xhtml": `<html><body><p>Book chapter</p></body></html>`})
	text, _, _ = run(t, "office.epub", epub, `{}`)
	if !strings.Contains(text, "book/chapter1.xhtml") {
		t.Fatal(text)
	}
	text, _, _ = run(t, "office.epub", epub, `{"member":"book/chapter1.xhtml"}`)
	if !strings.Contains(text, "Book chapter") {
		t.Fatal(text)
	}
}

func TestGiantNodesHaveResumablePages(t *testing.T) {
	t.Parallel()
	value := strings.Repeat("Привет", 4000)
	for _, operation := range []string{"document.xml", "document.html"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			input := fixture(t, "<p>"+value+"</p>")
			combined := readAllPages(t, operation, input)
			if combined != value+"\n" {
				t.Fatal("paging lost or duplicated text")
			}
		})
	}
}

func TestTARAndGZIPExtraction(t *testing.T) {
	t.Parallel()
	input := filepath.Join(t.TempDir(), "input.tar.gz")
	f, err := os.Create(input)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	w := tar.NewWriter(gz)
	if err = w.WriteHeader(&tar.Header{Name: "folder/a.txt", Mode: 0600, Size: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	if err = gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	data, _, _ := run(t, "archive.list", input, `{}`)
	if !strings.Contains(data, "folder/a.txt") {
		t.Fatal(data)
	}
	_, outputs, _, _, err := filetext.Process(
		t.Context(),
		"archive.extract",
		json.RawMessage(`{"member":"folder/a.txt"}`),
		input,
		t.TempDir(),
	)
	if err != nil || len(outputs) != 1 {
		t.Fatalf("TAR extract: %v", err)
	}
	content, err := os.ReadFile(outputs[0].Path)
	if err != nil || string(content) != "hello" {
		t.Fatalf("TAR content: %q %v", content, err)
	}
	input = filepath.Join(t.TempDir(), "content.gz")
	f, err = os.Create(input)
	if err != nil {
		t.Fatal(err)
	}
	gz = gzip.NewWriter(f)
	if _, err = gz.Write([]byte("standalone gzip")); err != nil {
		t.Fatal(err)
	}
	if err = gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	_, outputs, _, _, err = filetext.Process(
		t.Context(),
		"archive.extract",
		json.RawMessage(`{"member":"content"}`),
		input,
		t.TempDir(),
	)
	if err != nil || len(outputs) != 1 {
		t.Fatalf("GZIP extract: %v", err)
	}
	content, err = os.ReadFile(outputs[0].Path)
	if err != nil || string(content) != "standalone gzip" {
		t.Fatalf("GZIP content: %q %v", content, err)
	}
}

func TestRejectUnsafeOrMalformedInput(t *testing.T) {
	t.Parallel()
	cases := []struct{ operation, content, params string }{
		{"text.read", "\x00binary", `{}`},
		{"text.read", "hello", `{"cursor":"-1"}`},
		{"text.search", "hello", `{"query":"[","regex":true}`},
		{"document.json", `{"broken":`, `{}`},
		{"document.json", strings.Repeat("[", 129) + strings.Repeat("]", 129), `{}`},
		{"document.xml", `<!DOCTYPE x><x/>`, `{}`},
	}
	for _, item := range cases {
		t.Run(item.operation+item.content[:min(10, len(item.content))], func(t *testing.T) {
			t.Parallel()
			_, _, _, _, err := filetext.Process(
				t.Context(),
				item.operation,
				json.RawMessage(item.params),
				fixture(t, item.content),
				t.TempDir(),
			)
			if err == nil {
				t.Fatal("unsafe or malformed input accepted")
			}
		})
	}
	for _, name := range []string{"../escape", "C:/escape", "a\\escape"} {
		_, _, _, _, err := filetext.Process(
			t.Context(),
			"archive.list",
			json.RawMessage(`{}`),
			zipFixture(t, map[string]string{name: "x"}),
			t.TempDir(),
		)
		if err == nil {
			t.Fatalf("unsafe member accepted: %s", name)
		}
	}
	_, _, _, _, err := filetext.Process(
		t.Context(),
		"archive.list",
		json.RawMessage(`{}`),
		zipFixture(t, map[string]string{"bomb": strings.Repeat("a", 1<<20)}),
		t.TempDir(),
	)
	if err == nil {
		t.Fatal("compression bomb accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, _, _, err = filetext.Process(ctx, "text.read", nil, fixture(t, "hello"), t.TempDir())
	if err == nil {
		t.Fatal("canceled operation accepted")
	}
}

func readAllPages(t *testing.T, operation, input string) string {
	t.Helper()
	cursor := ""
	var combined strings.Builder
	for {
		parameters := map[string]any{"cursor": cursor, "limit": 1024}
		if operation == "office.pptx" {
			parameters["slide"] = 1
		}
		params, err := json.Marshal(parameters)
		if err != nil {
			t.Fatal(err)
		}
		data, _, partial, next, err := filetext.Process(t.Context(), operation, params, input, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		var page struct {
			Text   string `json:"text"`
			Slides []struct {
				Text string `json:"text"`
			} `json:"slides"`
		}
		if err = json.Unmarshal(data, &page); err != nil {
			t.Fatal(err)
		}
		combined.WriteString(page.Text)
		for _, slide := range page.Slides {
			combined.WriteString(slide.Text)
		}
		if !partial {
			break
		}
		if cursor == next {
			t.Fatal("cursor did not advance")
		}
		cursor = next
	}
	return combined.String()
}
