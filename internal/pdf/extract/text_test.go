package extract

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/m7medVision/docstomd-go/internal/pdf/parse"
)

// buildSFNT returns a minimal 54-byte sfnt whose only table is a cmap with a
// single format-6 subtable mapping GID 0x8148 to 'H' and GID 0x8149 to 'I'.
// sfntGIDToUnicode reads only the table directory and the cmap subtable, so
// no glyf/head tables are needed.
func buildSFNT() []byte {
	b := make([]byte, 0, 54)
	b = append(b, 0x00, 0x01, 0x00, 0x00)             // sfntVersion 1.0
	b = append(b, 0x00, 0x01)                         // numTables = 1
	b = append(b, 0x00, 0x10, 0x00, 0x00, 0x00, 0x00) // searchRange, entrySelector, rangeShift
	b = append(b, 'c', 'm', 'a', 'p')                 // tag
	b = append(b, 0, 0, 0, 0)                         // checksum (unchecked)
	b = append(b, 0, 0, 0, 28)                        // table offset
	b = append(b, 0, 0, 0, 14)                        // table length
	b = append(b, 0x00, 0x00)                         // cmap version 0
	b = append(b, 0x00, 0x01)                         // cmap numTables 1
	b = append(b, 0x00, 0x03, 0x00, 0x01)             // platform 3 (Windows), encoding 1 (Unicode BMP)
	b = append(b, 0, 0, 0, 12)                        // subtable offset, relative to cmap start
	b = append(b, 0x00, 0x06)                         // subtable format 6
	b = append(b, 0x00, 0x0e)                         // subtable length 14
	b = append(b, 0x00, 0x00)                         // language
	b = append(b, 0x00, 0x48)                         // firstCode 'H'
	b = append(b, 0x00, 0x02)                         // entryCount 2
	b = append(b, 0x81, 0x48)                         // glyph ID for 'H'
	b = append(b, 0x81, 0x49)                         // glyph ID for 'I'
	return b
}

// buildEmbeddedFontPDF assembles a complete PDF in memory with computed xref
// offsets (no repairByScan dependency). It carries two Type0/Identity-H fonts:
// F1 embeds an sfnt with a usable cmap via FontFile2; F2's CIDFont has no
// FontDescriptor at all.
func buildEmbeddedFontPDF(t *testing.T) []byte {
	t.Helper()
	fontFile := buildSFNT()
	var buf bytes.Buffer
	offsets := map[int]int{}
	obj := func(num int, body string) {
		offsets[num] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", num, body)
	}
	buf.WriteString("%PDF-1.4\n")
	obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	obj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	obj(3, "<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 4 0 R /F2 8 0 R >> >> >>")
	obj(4, "<< /Type /Font /Subtype /Type0 /BaseFont /Fake /Encoding /Identity-H /DescendantFonts [5 0 R] >>")
	obj(5, "<< /Type /Font /Subtype /CIDFontType2 /BaseFont /Fake /DW 1000 /FontDescriptor 6 0 R >>")
	obj(6, "<< /Type /FontDescriptor /FontName /Fake /Flags 4 /FontFile2 7 0 R >>")
	offsets[7] = buf.Len()
	fmt.Fprintf(&buf, "7 0 obj\n<< /Length %d >>\nstream\n", len(fontFile))
	buf.Write(fontFile)
	buf.WriteString("\nendstream\nendobj\n")
	obj(8, "<< /Type /Font /Subtype /Type0 /BaseFont /FakeNoDesc /Encoding /Identity-H /DescendantFonts [9 0 R] >>")
	obj(9, "<< /Type /Font /Subtype /CIDFontType2 /BaseFont /FakeNoDesc /DW 1000 >>")
	xrefOff := buf.Len()
	buf.WriteString("xref\n0 10\n0000000000 65535 f \n")
	for i := 1; i <= 9; i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer << /Size 10 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xrefOff)
	return buf.Bytes()
}

func TestEmbeddedFontFallback(t *testing.T) {
	doc, err := parse.Parse(buildEmbeddedFontPDF(t))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	resources := doc.PageResources(doc.Pages()[0])
	fonts, ok := dictOf(doc, firstNonNil(resources, "Font"))
	if !ok {
		t.Fatal("page font resource dict not found")
	}
	fontDict, ok := dictOf(doc, fonts["F1"])
	if !ok {
		t.Fatal("font F1 dict not found in page resources")
	}
	fc := buildFontContext(doc, fontDict)
	if fc.fallback == nil {
		t.Fatal("embedded FontFile2 with cmap did not produce a fallback cmap")
	}
	if s := fc.fallback.entries[0x8148]; s != "H" {
		t.Errorf("fallback.entries[0x8148] = %q, want %q", s, "H")
	}
	got, ok := fc.decodeRaw(doc, []byte{0x81, 0x48, 0x81, 0x49})
	if !ok {
		t.Fatal("decodeRaw reported failure for CID font with embedded cmap")
	}
	if got != "HI" {
		t.Errorf("decodeRaw = %q, want %q", got, "HI")
	}

	// Negative control: same shape, but the CIDFont has no FontDescriptor, so
	// there is no embedded file to mine and the fallback must stay nil.
	noDescDict, ok := dictOf(doc, fonts["F2"])
	if !ok {
		t.Fatal("font F2 dict not found in page resources")
	}
	fc2 := buildFontContext(doc, noDescDict)
	if fc2.fallback != nil {
		t.Fatal("CIDFont without FontDescriptor produced a fallback cmap")
	}
	if got, _ := fc2.decodeRaw(doc, []byte{0x81, 0x48, 0x81, 0x49}); got != "\uFFFD\uFFFD" {
		t.Errorf("no-descriptor decodeRaw = %q, want replacement chars", got)
	}
}

// TestFormXObjectFont ensures fonts declared only in a Form XObject's
// /Resources are loaded when the interpreter descends into the form. Some
// producers wrap page content in forms this way, so a font may exist solely
// at form level.
func TestFormXObjectFont(t *testing.T) {
	var buf bytes.Buffer
	offsets := map[int]int{}
	obj := func(num int, body string) {
		offsets[num] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", num, body)
	}
	buf.WriteString("%PDF-1.4\n")
	obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	obj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	obj(3, "<< /Type /Page /Parent 2 0 R /Contents 6 0 R /Resources << /XObject << /Fm0 4 0 R >> >> >>")
	offsets[4] = buf.Len()
	form := "BT /Tf0 12 Tf 10 100 Td (hello) Tj ET"
	fmt.Fprintf(&buf, "4 0 obj\n<< /Type /XObject /Subtype /Form /BBox [0 0 200 200] /Resources << /Font << /Tf0 5 0 R >> >> /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(form), form)
	obj(5, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	offsets[6] = buf.Len()
	page := "q /Fm0 Do Q"
	fmt.Fprintf(&buf, "6 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(page), page)
	xrefOff := buf.Len()
	buf.WriteString("xref\n0 7\n0000000000 65535 f \n")
	for i := 1; i <= 6; i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer << /Size 7 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xrefOff)

	doc, err := parse.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	pages := Extract(doc)
	if len(pages) != 1 {
		t.Fatalf("pages = %d, want 1", len(pages))
	}
	var texts []string
	for _, item := range pages[0].Items {
		if item.ItemType == ItemText {
			texts = append(texts, item.Text)
		}
	}
	if len(texts) != 1 || texts[0] != "hello" {
		t.Errorf("form text items = %q, want [hello]", texts)
	}
}
