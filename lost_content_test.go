package docstomd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// corruptFlatePDF builds a two-page PDF. Page 2's content stream claims
// /FlateDecode but holds plain operators, so decoding fails; its raw bytes
// would read as the text "garbage" if interpreted.
func corruptFlatePDF() []byte {
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
	obj(2, "<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>")
	obj(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 6 0 R /Resources << /Font << /F1 5 0 R >> >> >>")
	obj(4, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 7 0 R /Resources << /Font << /F1 5 0 R >> >> >>")
	obj(5, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	stream(6, "", "BT /F1 12 Tf 72 700 Td (The first page reads normally.) Tj ET")
	stream(7, "/Filter /FlateDecode", "BT /F1 12 Tf 72 700 Td (garbage) Tj ET")
	xrefOff := buf.Len()
	buf.WriteString("xref\n0 8\n0000000000 65535 f \n")
	for i := 1; i <= 7; i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer << /Size 8 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xrefOff)
	return buf.Bytes()
}

func TestConvertReportsLostContentPages(t *testing.T) {
	result, err := Convert(context.Background(), bytes.NewReader(corruptFlatePDF()), Options{})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if strings.Contains(result.Markdown, "garbage") {
		t.Errorf("undecoded stream bytes leaked into markdown: %q", result.Markdown)
	}
	if !strings.Contains(result.Markdown, "The first page reads normally.") {
		t.Errorf("intact page text missing from markdown: %q", result.Markdown)
	}
	if want := []int{2}; !slices.Equal(result.LostContentPages, want) {
		t.Errorf("LostContentPages = %v, want %v", result.LostContentPages, want)
	}
}

// TestConvertFixturesReportNoLostContent guards that well-formed fixtures
// never flag lost content; with no flag set, extraction ran exactly as before.
func TestConvertFixturesReportNoLostContent(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "*", "*.pdf"))
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
			result, err := Convert(context.Background(), bytes.NewReader(data), Options{})
			var typed *Error
			if errors.As(err, &typed) && (typed.Code == CodeNeedsOcr || typed.Code == CodeEncrypted) {
				return
			}
			if err != nil {
				t.Fatalf("Convert: %v", err)
			}
			if len(result.LostContentPages) != 0 {
				t.Errorf("LostContentPages = %v, want none", result.LostContentPages)
			}
		})
	}
}
