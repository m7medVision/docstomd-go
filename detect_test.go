package docstomd

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

var oleHeader = append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, make([]byte, 56)...)

func buildZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const relFmt = `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="%s"/></Relationships>`

func TestDetectContentMagic(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  Format
	}{
		{"rtf header", `{\rtf1\ansi`, FormatRtf},
		{"rtf wins over embedded pdf literal", `{\rtf1\ansi %PDF-1.4 mentioned}`, FormatRtf},
		{"ole header", string(oleHeader), FormatOle},
		{"garbage", "plain text, nothing else", FormatUnknown},
		{"empty", "", FormatUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			det, err := Detect(context.Background(), bytes.NewReader([]byte(tc.input)), "")
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if det.Format != tc.want {
				t.Errorf("format = %v, want %v", det.Format, tc.want)
			}
		})
	}
}

func TestDetectFakePDFBytesAreMalformed(t *testing.T) {
	_, err := Detect(context.Background(), bytes.NewReader([]byte("%PDF-1.7\nnot really")), "")
	if err == nil {
		t.Fatal("fake PDF bytes must not detect successfully")
	}
	if got := ErrorCodeOf(err); got != CodeMalformed {
		t.Errorf("code = %q, want %q", got, CodeMalformed)
	}
}

func TestDetectContentWinsOverExtension(t *testing.T) {
	_, err := Detect(context.Background(), bytes.NewReader([]byte("%PDF-1.4 pdf bytes")), "report.docx")
	if err == nil || ErrorCodeOf(err) != CodeMalformed {
		t.Fatalf("PDF content must route to the PDF pipeline regardless of name, got %v", err)
	}
}

func TestDetectOPCRelationships(t *testing.T) {
	cases := []struct {
		target string
		part   string
		want   Format
	}{
		{"word/document.xml", "<w:document/>", FormatDocx},
		{"xl/workbook.xml", "<workbook/>", FormatXlsx},
		{"ppt/presentation.xml", "<p:presentation/>", FormatPptx},
	}
	for _, tc := range cases {
		t.Run(tc.target, func(t *testing.T) {
			data := buildZip(t, map[string]string{
				"_rels/.rels":         fmt.Sprintf(relFmt, tc.target),
				tc.target:             tc.part,
				"[Content_Types].xml": "<Types/>",
			})
			det, err := Detect(context.Background(), bytes.NewReader(data), "")
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if det.Format != tc.want {
				t.Errorf("format = %v, want %v", det.Format, tc.want)
			}
		})
	}
}

func TestDetectOPCRelocatedMainPart(t *testing.T) {
	const ctFmt = `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
		`<Default Extension="xml" ContentType="application/xml"/>%s</Types>`
	override := func(ct string) string {
		return fmt.Sprintf(ctFmt, `<Override PartName="/content/Main.XML" ContentType="`+ct+`"/>`)
	}
	cases := []struct {
		name  string
		types string
		root  string
		want  Format
	}{
		{"docx by content type", override("application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"), "<root/>", FormatDocx},
		{"docm by content type", override("application/vnd.ms-word.document.macroEnabled.main+xml"), "<root/>", FormatDocx},
		{"xlsx by content type", override("application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"), "<root/>", FormatXlsx},
		{"xlsb content type is not xlsx", override("application/vnd.ms-excel.sheet.binary.macroEnabled.main"), "<root/>", FormatUnknown},
		{"ppsx by content type", override("application/vnd.openxmlformats-officedocument.presentationml.slideshow.main+xml"), "<root/>", FormatPptx},
		{"docx by root element", fmt.Sprintf(ctFmt, ""), `<?xml version="1.0"?><!-- lead --><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"/>`, FormatDocx},
		{"docx by strict root element under a generic content type", override("application/xml"), `<w:document xmlns:w="http://purl.oclc.org/ooxml/wordprocessingml/main"/>`, FormatDocx},
		{"xlsx by strict root element", fmt.Sprintf(ctFmt, ""), `<workbook xmlns="http://purl.oclc.org/ooxml/spreadsheetml/main"/>`, FormatXlsx},
		{"pptx by root element", fmt.Sprintf(ctFmt, ""), `<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"/>`, FormatPptx},
		{"unknown root element", fmt.Sprintf(ctFmt, ""), `<p:presentation xmlns:p="urn:example"/>`, FormatUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := buildZip(t, map[string]string{
				"_rels/.rels":         fmt.Sprintf(relFmt, "/content/Main.XML"),
				"content/Main.XML":    tc.root,
				"[Content_Types].xml": tc.types,
			})
			det, err := Detect(context.Background(), bytes.NewReader(data), "")
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if det.Format != tc.want {
				t.Errorf("format = %v, want %v", det.Format, tc.want)
			}
		})
	}
}

func TestDetectOPCConventionalPaths(t *testing.T) {
	cases := map[string]Format{
		"word/document.xml":    FormatDocx,
		"xl/workbook.xml":      FormatXlsx,
		"ppt/presentation.xml": FormatPptx,
	}
	for part, want := range cases {
		t.Run(part, func(t *testing.T) {
			data := buildZip(t, map[string]string{part: "<root/>"})
			det, err := Detect(context.Background(), bytes.NewReader(data), "")
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if det.Format != want {
				t.Errorf("format = %v, want %v", det.Format, want)
			}
		})
	}
}

func TestDetectNonOfficeZipIsUnknown(t *testing.T) {
	data := buildZip(t, map[string]string{
		"mimetype":    "application/vnd.oasis.opendocument.text",
		"content.xml": "<office:document-content/>",
	})
	det, err := Detect(context.Background(), bytes.NewReader(data), "")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if det.Format != FormatUnknown {
		t.Errorf("format = %v, want unknown", det.Format)
	}

	plain := buildZip(t, map[string]string{"hello.txt": "hi"})
	det, err = Detect(context.Background(), bytes.NewReader(plain), "")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if det.Format != FormatUnknown {
		t.Errorf("plain zip: format = %v, want unknown", det.Format)
	}
}

func TestDetectExtensionFallbackOnlyWhenInconclusive(t *testing.T) {
	plain := buildZip(t, map[string]string{"hello.txt": "hi"})
	det, err := Detect(context.Background(), bytes.NewReader(plain), "notes.docx")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if det.Format != FormatUnknown {
		t.Errorf("parseable non-office zip: format = %v, want unknown (content is conclusive)", det.Format)
	}

	corruptZip := append([]byte("PK\x03\x04"), []byte("truncated central directory")...)
	det, err = Detect(context.Background(), bytes.NewReader(corruptZip), "notes.docx")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if det.Format != FormatDocx {
		t.Errorf("corrupt zip: format = %v, want docx via name", det.Format)
	}

	det, err = Detect(context.Background(), bytes.NewReader([]byte("some bytes")), "legacy.doc")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if det.Format != FormatOle {
		t.Errorf("named .doc: format = %v, want ole via name", det.Format)
	}
}

func TestDetectConformanceFixtures(t *testing.T) {
	manifest := map[string]Format{
		"broken_startxref_pointer.pdf":          FormatPDF,
		"author_block_superscripts.pdf":         FormatPDF,
		"cropbox_offset_origin.pdf":             FormatPDF,
		"encrypted-secret123.pdf":               FormatPDF,
		"forecast_table_chart.pdf":              FormatPDF,
		"handmade-cocoa.rtf":                    FormatRtf,
		"handmade-links.pptx":                   FormatPptx,
		"handmade-merged.xlsx":                  FormatXlsx,
		"handmade-mixed.pdf":                    FormatPDF,
		"handmade-multimaster.ppt":              FormatOle,
		"handmade-rich.docx":                    FormatDocx,
		"handmade-scanned.pdf":                  FormatPDF,
		"handmade-strict.docx":                  FormatDocx,
		"pres.pptx":                             FormatPptx,
		"scan_with_native_header_text.pdf":      FormatPDF,
		"sheet.xls":                             FormatOle,
		"sheet.xlsx":                            FormatXlsx,
		"shifted_cipher_tounicode.pdf":          FormatPDF,
		"shinagawa_identity_h.pdf":              FormatPDF,
		"td9264.pdf":                            FormatPDF,
		"text.doc":                              FormatOle,
		"text.docx":                             FormatDocx,
		"text_page_with_watermark_image.pdf":    FormatPDF,
		"thermo-freon12.pdf":                    FormatPDF,
		"vector_outlined_text_with_caption.pdf": FormatPDF,
		"wireless_two_col_no_rects.pdf":         FormatPDF,
	}
	entries, err := os.ReadDir(filepath.Join("testdata", "detect"))
	if err != nil {
		t.Fatal(err)
	}
	knownMeta := map[string]bool{"NOTICE.md": true, "pdf-goldens.json": true}
	for _, entry := range entries {
		if !knownMeta[entry.Name()] {
			if _, ok := manifest[entry.Name()]; !ok {
				t.Errorf("testdata/detect/%s is not in the manifest (add it when adding fixtures)", entry.Name())
			}
		}
	}
	if len(entries) != len(manifest)+len(knownMeta) {
		t.Fatalf("testdata/detect has %d files, manifest covers %d (update manifest when adding fixtures)", len(entries), len(manifest))
	}
	for name, want := range manifest {
		data, err := os.ReadFile(filepath.Join("testdata", "detect", name))
		if err != nil {
			t.Errorf("fixture %s: %v", name, err)
			continue
		}
		det, err := Detect(context.Background(), bytes.NewReader(data), "")
		if err != nil {
			if want == FormatPDF && ErrorCodeOf(err) == CodeEncrypted {
				continue
			}
			t.Errorf("%s: Detect: %v", name, err)
			continue
		}
		if det.Format != want {
			t.Errorf("%s: format = %v, want %v", name, det.Format, want)
		}
	}
}

func TestConvertFakePDFBytesAreMalformed(t *testing.T) {
	_, err := Convert(context.Background(), bytes.NewReader([]byte("%PDF-1.4 not really")), Options{})
	if got := ErrorCodeOf(err); got != CodeMalformed {
		t.Errorf("code = %q, want %q", got, CodeMalformed)
	}
}

func TestConvertExplicitFormatOverridesDetection(t *testing.T) {
	_, err := Convert(context.Background(), bytes.NewReader([]byte("%PDF-1.4 not really")), Options{Format: FormatRtf})
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("want typed error, got %v", err)
	}
	if want := "unsupported input format: rtf"; e.Message != want {
		t.Errorf("message = %q, want %q", e.Message, want)
	}
}
