package extract

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/m7medVision/docstomd-go/internal/pdf/internal/fuzzguard"
	"github.com/m7medVision/docstomd-go/internal/pdf/parse"
)

const (
	fuzzHeapBudget      = 256 << 20
	fuzzTimeBudget      = 5 * time.Second
	fuzzMaxContent      = 1 << 16
	fuzzSeedPagesPerDoc = 3
)

// buildContentPDF wraps content as the only page's content stream. The page
// offers a simple base-14 font (F1), a Type0 Identity-H font with an embedded
// sfnt (F2), and a Form XObject (Fm0) so fuzzed operators reach every font
// and XObject path the interpreter has.
func buildContentPDF(content []byte) []byte {
	fontFile := buildSFNT()
	form := "BT /F1 10 Tf 5 5 Td (form) Tj ET"
	var buf bytes.Buffer
	offsets := map[int]int{}
	obj := func(num int, body string) {
		offsets[num] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", num, body)
	}
	stream := func(num int, dict string, data []byte) {
		offsets[num] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n<< %s /Length %d >>\nstream\n", num, dict, len(data))
		buf.Write(data)
		buf.WriteString("\nendstream\nendobj\n")
	}
	buf.WriteString("%PDF-1.4\n")
	obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	obj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	obj(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 10 0 R /Resources << /Font << /F1 4 0 R /F2 5 0 R >> /XObject << /Fm0 9 0 R >> >> >>")
	obj(4, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	obj(5, "<< /Type /Font /Subtype /Type0 /BaseFont /Fake /Encoding /Identity-H /DescendantFonts [6 0 R] >>")
	obj(6, "<< /Type /Font /Subtype /CIDFontType2 /BaseFont /Fake /DW 1000 /FontDescriptor 7 0 R >>")
	obj(7, "<< /Type /FontDescriptor /FontName /Fake /Flags 4 /FontFile2 8 0 R >>")
	stream(8, "", fontFile)
	stream(9, "/Type /XObject /Subtype /Form /BBox [0 0 200 200] /Resources << /Font << /F1 4 0 R >> >>", []byte(form))
	stream(10, "", content)
	xrefOff := buf.Len()
	buf.WriteString("xref\n0 11\n0000000000 65535 f \n")
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer << /Size 11 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xrefOff)
	return buf.Bytes()
}

// addFixtureContentSeeds seeds with the decoded content streams of the first
// pages of every PDF fixture.
func addFixtureContentSeeds(f *testing.F) {
	f.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "..", "..", "testdata", "*", "*.pdf"))
	if err != nil {
		f.Fatalf("glob fixtures: %v", err)
	}
	if len(paths) == 0 {
		f.Fatal("no PDF fixtures found under testdata")
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			f.Fatalf("read fixture %s: %v", path, err)
		}
		doc, err := parse.Parse(data)
		if err != nil {
			continue
		}
		pages := doc.Pages()
		if len(pages) > fuzzSeedPagesPerDoc {
			pages = pages[:fuzzSeedPagesPerDoc]
		}
		for _, page := range pages {
			for _, contentRef := range doc.PageContents(page) {
				obj, err := doc.GetObject(contentRef.Num)
				if err != nil {
					continue
				}
				content, ok := doc.StreamData(obj)
				if !ok || len(content) > fuzzMaxContent {
					continue
				}
				f.Add(bytes.Clone(content))
			}
		}
	}
	f.Add([]byte("BT /F1 12 Tf 72 700 Td (Hello) Tj ET"))
	f.Add([]byte("BT /F2 12 Tf 1 0 0 1 72 700 Tm <81488149> Tj ET"))
	f.Add([]byte("q 1 0 0 1 10 10 cm /Fm0 Do Q 0 0 100 100 re S"))
	f.Add([]byte(strings.Repeat("[", 20_000) + " BT /F1 12 Tf (after) Tj ET"))
	f.Add([]byte(strings.Repeat("<< /A ", 5_000) + " BT /F1 12 Tf (after) Tj ET"))
	f.Add([]byte(strings.Repeat("q ", 10_000) + "BT /F1 12 Tf (deep) Tj ET " + strings.Repeat("Q ", 10_000)))
	f.Add([]byte("[1 +Inf -Infinity] 0 0 1 0 0 cm 0 0 +Inf 1 re f"))
	// " with fewer than three operands (ticket 05).
	f.Add([]byte(`((\8((()(0"(000`))
	f.Add([]byte(`BT /F1 12 Tf 72 700 Td (abc) " (keep) Tj ET`))
}

// FuzzInterpret runs arbitrary content-stream bytes through text extraction.
// It asserts only that nothing panics, hangs, or exhausts memory.
func FuzzInterpret(f *testing.F) {
	addFixtureContentSeeds(f)
	guard := fuzzguard.Start(f, fuzzHeapBudget, fuzzTimeBudget)
	f.Fuzz(func(t *testing.T, content []byte) {
		if len(content) > fuzzMaxContent {
			t.Skip("input larger than fuzz cap")
		}
		guard.Begin()
		defer guard.End()
		doc, err := parse.Parse(buildContentPDF(content))
		if err != nil {
			t.Fatalf("parse wrapper PDF: %v", err)
		}
		Extract(doc)
	})
}

func TestBuildContentPDFExtractsText(t *testing.T) {
	doc, err := parse.Parse(buildContentPDF([]byte("BT /F1 12 Tf 72 700 Td (Hello) Tj ET BT /F2 12 Tf 72 600 Td <81488149> Tj ET q /Fm0 Do Q")))
	if err != nil {
		t.Fatalf("parse wrapper PDF: %v", err)
	}
	pages := Extract(doc)
	if len(pages) != 1 {
		t.Fatalf("got %d pages, want 1", len(pages))
	}
	var texts []string
	for _, item := range pages[0].Items {
		texts = append(texts, item.Text)
	}
	joined := strings.Join(texts, " ")
	for _, want := range []string{"Hello", "HI", "form"} {
		if !strings.Contains(joined, want) {
			t.Errorf("extracted text %q is missing %q", joined, want)
		}
	}
}
