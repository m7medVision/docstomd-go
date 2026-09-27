package markdown

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/m7medVision/docstomd-go/internal/pdf/extract"
	"github.com/m7medVision/docstomd-go/internal/pdf/parse"
)

// buildPDF assembles a one-page PDF with content drawn in F1, a base-14 font
// with WinAnsiEncoding named baseFont.
func buildPDF(baseFont, content string) []byte {
	var buf bytes.Buffer
	offsets := map[int]int{}
	obj := func(num int, body string) {
		offsets[num] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", num, body)
	}
	buf.WriteString("%PDF-1.4\n")
	obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	obj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	obj(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 5 0 R /Resources << /Font << /F1 4 0 R >> >> >>")
	obj(4, "<< /Type /Font /Subtype /Type1 /BaseFont /"+baseFont+" /Encoding /WinAnsiEncoding >>")
	obj(5, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
	xrefOff := buf.Len()
	buf.WriteString("xref\n0 6\n0000000000 65535 f \n")
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer << /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xrefOff)
	return buf.Bytes()
}

func convertPDF(t *testing.T, data []byte) string {
	t.Helper()
	doc, err := parse.Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	md, _ := Convert(extract.Extract(doc), DefaultOptions())
	return md
}

func TestLoneHyphenLineConverts(t *testing.T) {
	tests := []struct {
		name     string
		baseFont string
		content  string
		want     string
	}{
		{
			// RemovePageNums drops a line that is only "-", so this
			// converts without reaching fixHyphenation.
			name:     "Courier line",
			baseFont: "Courier",
			content:  "BT /F1 10 Tf 72 700 Td (x = 1) Tj 0 -12 Td (-) Tj 0 -12 Td (next line) Tj ET",
			want:     "```\nx = 1\nnext line\n```\n",
		},
		{
			// Line breaks inside one shown string survive into the
			// Markdown, so fixHyphenation sees a line that is only "-"
			// followed by a lowercase line and must not index before it.
			name:     "line break inside a string",
			baseFont: "Helvetica",
			content:  "BT /F1 10 Tf 72 700 Td (abc\\n-\\nnext) Tj ET",
			want:     "abc\n-\nnext\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := convertPDF(t, buildPDF(tt.baseFont, tt.content)); got != tt.want {
				t.Errorf("markdown = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNonASCIIHyphenatedWordIsRejoined(t *testing.T) {
	// WinAnsi 0xE8 is è; the byte before the hyphen is the second byte
	// of its UTF-8 encoding, so the check must decode the last rune.
	content := "BT /F1 10 Tf 72 700 Td (x = tr\xe8s-) Tj 0 -12 Td (bien) Tj ET"
	want := "```\nx = tr\u00e8s\nbien\n```\n"
	if got := convertPDF(t, buildPDF("Courier", content)); got != want {
		t.Errorf("markdown = %q, want %q", got, want)
	}
}

func TestFixHyphenation(t *testing.T) {
	tests := []struct {
		name string
		md   string
		want string
	}{
		{name: "ascii word", md: "exam-\nple", want: "exam\nple"},
		{name: "non-ascii last letter", md: "très-\nbien", want: "très\nbien"},
		{name: "lone hyphen", md: "-\nnext", want: "-\nnext"},
		{name: "hyphen after space", md: "a -\nnext", want: "a -\nnext"},
		{name: "uppercase before hyphen", md: "A-\nnext", want: "A-\nnext"},
		{name: "uppercase next line", md: "exam-\nPle", want: "exam-\nPle"},
		{name: "trailing spaces trimmed", md: "exam-  \n  ple", want: "exam\nple"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fixHyphenation(tt.md); got != tt.want {
				t.Errorf("fixHyphenation(%q) = %q, want %q", tt.md, got, tt.want)
			}
		})
	}
}
