package docstomd

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestConvertXlsxMatchesGoldens(t *testing.T) {
	for _, name := range []string{"sheet.xlsx", "handmade-merged.xlsx"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "detect", name))
			if err != nil {
				t.Fatal(err)
			}
			golden, err := os.ReadFile(filepath.Join("testdata", "office-goldens", name+".md"))
			if err != nil {
				t.Fatal(err)
			}
			result, err := Convert(context.Background(), bytes.NewReader(data), Options{})
			if err != nil {
				t.Fatalf("Convert: %v", err)
			}
			if result.Markdown != string(golden) {
				t.Errorf("markdown mismatch\ngot:\n%s\nwant:\n%s", result.Markdown, golden)
			}
			if result.Format != FormatXlsx {
				t.Errorf("format = %v, want xlsx", result.Format)
			}
		})
	}
}

func TestConvertXlsxMapsErrors(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, _ := zw.Create("xl/workbook.xml")
	_, _ = f.Write([]byte(`<?xml version="1.0"?><document/>`))
	_ = zw.Close()
	_, err := Convert(context.Background(), bytes.NewReader(buf.Bytes()), Options{Format: FormatXlsx})
	if got := ErrorCodeOf(err); got != CodeMalformed {
		t.Errorf("non-workbook main part: code = %q (%v), want malformed", got, err)
	}
}
