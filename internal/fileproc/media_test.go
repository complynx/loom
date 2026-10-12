package fileproc_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/complynx/loom/internal/fileproc"
)

func needCommands(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip("CLI unavailable: " + name)
		}
	}
}
func writePDF(t *testing.T, path string) {
	writePDFText(t, path, false)
}
func writePDFText(t *testing.T, path string, unicodeText bool) {
	writePDFContent(t, path, unicodeText, "BT /F1 18 Tf 20 100 Td (hello agent) Tj ET")
}
func writePDFContent(t *testing.T, path string, unicodeText bool, content string) {
	t.Helper()
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
	}
	if unicodeText {
		unicodeMap := "/CIDInit /ProcSet findresource begin 12 dict begin begincmap /CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def /CMapName /Unicode def /CMapType 2 def 1 begincodespacerange <00> <FF> endcodespacerange 1 beginbfchar <61> <0430> endbfchar endcmap CMapName currentdict /CMap defineresource pop end end"
		unicodeContent := "BT /F1 18 Tf 0 100 Td <61> Tj ET"
		objects[3] = "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /ToUnicode 6 0 R >>"
		objects[4] = fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(unicodeContent), unicodeContent)
		objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(unicodeMap), unicodeMap))
	}
	offsets := []int{0}
	for i, obj := range objects {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&b, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	if err := os.WriteFile(path, b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestPDFCLIHappyPaths(t *testing.T) {
	t.Parallel()
	needCommands(t, "pdfinfo", "pdftotext", "pdftoppm")
	dir := t.TempDir()
	input := filepath.Join(dir, "input.pdf")
	writePDF(t, input)
	for _, operation := range []string{"pdf.info", "pdf.text", "pdf.search", "pdf.render"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			result, err := fileproc.Process(
				t.Context(),
				fileproc.Request{Operation: operation, Params: json.RawMessage(`{"query":"agent"}`)},
				input,
				t.TempDir(),
			)
			if err != nil {
				t.Fatal(err)
			}
			if operation == "pdf.render" {
				if len(result.Outputs) != 1 {
					t.Fatal("missing page")
				}
			} else if operation != "pdf.info" && !strings.Contains(string(result.Data), "agent") {
				t.Fatal("missing text")
			}
		})
	}
	result, err := fileproc.Process(
		t.Context(),
		fileproc.Request{Operation: "pdf.text", Params: json.RawMessage(`{"max_chars":4}`)},
		input,
		dir,
	)
	if err != nil || !result.Partial || result.NextCursor != "4" {
		t.Fatalf("partial=%+v err=%v", result, err)
	}
}
func TestGIFCLIPagesAndFrame(t *testing.T) {
	t.Parallel()
	needCommands(t, "ffprobe", "ffmpeg")
	dir := t.TempDir()
	input := filepath.Join(dir, "input.gif")
	palette := color.Palette{color.Black, color.White}
	first := image.NewPaletted(image.Rect(0, 0, 8, 8), palette)
	second := image.NewPaletted(image.Rect(0, 0, 8, 8), palette)
	second.SetColorIndex(2, 2, 1)
	file, err := os.Create(input)
	if err != nil {
		t.Fatal(err)
	}
	err = gif.EncodeAll(file, &gif.GIF{Image: []*image.Paletted{first, second}, Delay: []int{100, 100}})
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	result, err := fileproc.Process(
		t.Context(),
		fileproc.Request{
			Operation: "video.storyboard",
			Params:    json.RawMessage(`{"end":2,"interval":0.5,"max_frames":2}`),
		},
		input,
		dir,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Outputs) != 2 || !result.Partial || result.NextCursor != "1.000" {
		t.Fatalf("result %+v", result)
	}
	result, err = fileproc.Process(t.Context(), fileproc.Request{Operation: "video.frame"}, input, dir)
	if err != nil || len(result.Outputs) != 1 {
		t.Fatalf("frame %+v err=%v", result, err)
	}
}

func TestPDFUnicodePaginationRejectsNonprogress(t *testing.T) {
	t.Parallel()
	needCommands(t, "pdftotext")
	dir := t.TempDir()
	input := filepath.Join(dir, "unicode.pdf")
	writePDFText(t, input, true)
	for _, params := range []json.RawMessage{json.RawMessage(`{"max_chars":1}`), json.RawMessage(`{"max_chars":4,"offset":1}`)} {
		if result, err := fileproc.Process(
			t.Context(),
			fileproc.Request{Operation: "pdf.text", Params: params},
			input,
			t.TempDir(),
		); err == nil {
			t.Fatalf("expected UTF-8 boundary error, got %+v", result)
		}
	}
	result, err := fileproc.Process(
		t.Context(),
		fileproc.Request{Operation: "pdf.text", Params: json.RawMessage(`{"max_chars":2}`)},
		input,
		t.TempDir(),
	)
	if err != nil || !result.Partial || result.NextCursor != "2" || !strings.Contains(string(result.Data), "а") {
		t.Fatalf("result %+v error %v", result, err)
	}
}

func TestPDFSearchFindsBoundaryMatchesOnce(t *testing.T) {
	t.Parallel()
	needCommands(t, "pdftotext")
	for _, regex := range []bool{false, true} {
		t.Run(
			map[bool]string{false: "literal", true: "regex"}[regex],
			func(t *testing.T) { t.Parallel(); assertPDFBoundarySearch(t, regex) },
		)
	}
}
func assertPDFBoundarySearch(t *testing.T, regex bool) {
	t.Helper()
	dir := t.TempDir()
	input := filepath.Join(dir, "boundary.pdf")
	writePDFContent(t, input, false, "BT /F1 18 Tf 20 150 Td (12345678) Tj 0 -30 Td (abcdefXYZ) Tj ET")
	query := "XYZ"
	if regex {
		query = "X[Y]Z"
	}
	offset := int64(0)
	found := 0
	finished := false
	for range 16 {
		params, err := json.Marshal(map[string]any{"max_chars": 8, "offset": offset, "query": query, "regex": regex})
		if err != nil {
			t.Fatal(err)
		}
		result, err := fileproc.Process(
			t.Context(),
			fileproc.Request{Operation: "pdf.search", Params: params},
			input,
			t.TempDir(),
		)
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Matches []struct {
				Text string `json:"text"`
			} `json:"matches"`
		}
		if err = json.Unmarshal(result.Data, &body); err != nil {
			t.Fatal(err)
		}
		found += len(body.Matches)
		if !result.Partial {
			finished = true
			break
		}
		next, err := strconv.ParseInt(result.NextCursor, 10, 64)
		if err != nil || next <= offset {
			t.Fatalf("nonprogress cursor: %q previous %d", result.NextCursor, offset)
		}
		offset = next
	}
	if !finished || found != 1 {
		t.Fatalf("finished=%v matches=%d", finished, found)
	}
}
