package extract

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/m7medVision/docstomd-go/internal/pdf/parse"
)

// buildLostPDF builds a one-page PDF whose page content (object 10) and Form
// XObject Fm0 (object 9) carry the given extra stream dict entries. A page
// content ref of 99 points at an object that does not exist.
func buildLostPDF(contentDict, formDict string, contentRef int) []byte {
	content := "BT /F1 12 Tf 72 700 Td (pagetext) Tj ET q /Fm0 Do Q"
	form := "BT /F1 10 Tf 5 5 Td (formtext) Tj ET"
	var buf bytes.Buffer
	offsets := map[int]int{}
	obj := func(num int, body string) {
		offsets[num] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", num, body)
	}
	stream := func(num int, dict, data string) {
		offsets[num] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n<< %s /Length %d >>\nstream\n%s\nendstream\nendobj\n", num, dict, len(data), data)
	}
	buf.WriteString("%PDF-1.4\n")
	obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	obj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	obj(3, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents %d 0 R /Resources << /Font << /F1 4 0 R >> /XObject << /Fm0 9 0 R >> >> >>", contentRef))
	obj(4, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	for num := 5; num <= 8; num++ {
		obj(num, "null")
	}
	stream(9, "/Type /XObject /Subtype /Form /BBox [0 0 200 200] /Resources << /Font << /F1 4 0 R >> >> "+formDict, form)
	stream(10, contentDict, content)
	xrefOff := buf.Len()
	buf.WriteString("xref\n0 11\n0000000000 65535 f \n")
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer << /Size 11 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xrefOff)
	return buf.Bytes()
}

// TestExtractReportsLostContent checks that a stream which fails to decode
// is skipped (its raw bytes are never read as operators) and flagged, while
// intact streams still extract and report no loss.
func TestExtractReportsLostContent(t *testing.T) {
	tests := []struct {
		name        string
		contentDict string
		formDict    string
		contentRef  int
		wantText    string
		wantLost    bool
	}{
		{name: "well formed", contentRef: 10, wantText: "pagetext formtext"},
		{name: "corrupt page flate", contentDict: "/Filter /FlateDecode", contentRef: 10, wantLost: true},
		{name: "corrupt form flate", formDict: "/Filter /FlateDecode", contentRef: 10, wantText: "pagetext", wantLost: true},
		{name: "unsupported page filter", contentDict: "/Filter /JBIG2Decode", contentRef: 10, wantLost: true},
		{name: "missing content object", contentRef: 99, wantLost: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := parse.Parse(buildLostPDF(tt.contentDict, tt.formDict, tt.contentRef))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			pages := Extract(doc)
			if len(pages) != 1 {
				t.Fatalf("got %d pages, want 1", len(pages))
			}
			var texts []string
			for _, item := range pages[0].Items {
				texts = append(texts, item.Text)
			}
			if got := strings.Join(texts, " "); got != tt.wantText {
				t.Errorf("text = %q, want %q", got, tt.wantText)
			}
			if pages[0].LostContent != tt.wantLost {
				t.Errorf("LostContent = %v, want %v", pages[0].LostContent, tt.wantLost)
			}
		})
	}
}

// TestExtractFixturesReportNoLostContent sweeps every PDF fixture, including
// the ones Convert refuses as needsOcr, and expects no page flagged lost.
func TestExtractFixturesReportNoLostContent(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "..", "testdata", "*", "*.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no PDF fixtures found")
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			doc, err := parse.Parse(data)
			if err != nil {
				t.Skipf("fixture does not parse: %v", err)
			}
			for i, page := range Extract(doc) {
				if page.LostContent {
					t.Errorf("page %d flagged LostContent", i+1)
				}
			}
		})
	}
}
