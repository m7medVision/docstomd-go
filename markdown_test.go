package docstomd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

func normalizeMD(s string) string {
	s = regexp.MustCompile(`[|#*`+"`"+`>]`).ReplaceAllString(s, " ")
	s = regexp.MustCompile(`<sup>|</sup>|<sub>|</sub>`).ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}

func similarityRatio(a, b string) float64 {
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	cur := make([]int, len(br)+1)
	for i := 1; i <= len(ar); i++ {
		for j := 1; j <= len(br); j++ {
			if ar[i-1] == br[j-1] {
				cur[j] = prev[j-1] + 1
			} else if prev[j] >= cur[j-1] {
				cur[j] = prev[j]
			} else {
				cur[j] = cur[j-1]
			}
		}
		prev, cur = cur, prev
	}
	return 2 * float64(prev[len(br)]) / float64(len(ar)+len(br))
}

func TestConvertMarkdownConformance(t *testing.T) {
	needsOcr := map[string]bool{
		"handmade-mixed": true, "handmade-scanned": true,
		"scan_with_native_header_text": true, "vector_outlined_text_with_caption": true,
	}
	thresholds := map[string]float64{
		"cropbox_offset_origin":          1.0,
		"broken_startxref_pointer":       1.0,
		"author_block_superscripts":      0.95,
		"text_page_with_watermark_image": 0.95,
		"wireless_two_col_no_rects":      0.85,
	}
	contentFloor := map[string]float64{
		"thermo-freon12":           0.4,
		"shifted_cipher_tounicode": 0.4,
		"forecast_table_chart":     0.6,
		"td9264":                   0.7,
	}
	entries, err := os.ReadDir(filepath.Join("testdata", "md-goldens"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 13 {
		t.Fatalf("expected the golden corpus, found %d", len(entries))
	}
	for _, entry := range entries {
		if entry.Name() == "NOTICE.md" {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".md")
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "detect", name+".pdf"))
			if err != nil {
				t.Fatal(err)
			}
			result, err := Convert(context.Background(), bytes.NewReader(data), Options{})
			if needsOcr[name] {
				if err == nil {
					t.Fatalf("golden expects needsOcr, got markdown %q", result.Markdown[:min(60, len(result.Markdown))])
				}
				var typed *Error
				if !errors.As(err, &typed) || typed.Code != CodeNeedsOcr || len(typed.Pages) == 0 {
					t.Fatalf("want typed needsOcr with pages, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Convert: %v", err)
			}
			golden, err := os.ReadFile(filepath.Join("testdata", "md-goldens", entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			got, want := normalizeMD(result.Markdown), normalizeMD(string(golden))
			ratio := similarityRatio(got, want)
			threshold, tracked := thresholds[name]
			if name == "shinagawa_identity_h" {
				threshold = 1.0
			} else if !tracked {
				threshold = contentFloor[name]
			}
			if ratio < threshold {
				t.Errorf("markdown similarity %.3f below %.2f (got %d chars, want %d chars)", ratio, threshold, len(got), len(want))
			}
			if result.Format != FormatPDF || result.PageCount == 0 {
				t.Errorf("metadata: format=%v page_count=%d", result.Format, result.PageCount)
			}
		})
	}
}

func TestConvertNeedsOcrContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "detect", "handmade-scanned.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Convert(context.Background(), bytes.NewReader(data), Options{})
	var typed *Error
	if !errors.As(err, &typed) {
		t.Fatalf("want typed error, got %v", err)
	}
	if typed.Code != CodeNeedsOcr {
		t.Errorf("code = %q, want needsOcr", typed.Code)
	}
	if len(typed.Pages) != 2 || typed.PageCount != 2 {
		t.Errorf("pages=%v count=%d, want [1 2] of 2", typed.Pages, typed.PageCount)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestQualityCorpusFlags(t *testing.T) {
	expectedFlaggedPages := map[string][]int{
		"shifted_cipher_tounicode.pdf": {1},
		"shinagawa_identity_h.pdf":     {1},
	}
	entries, err := os.ReadDir(filepath.Join("testdata", "detect"))
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".pdf") || entry.Name() == "encrypted-secret123.pdf" {
			continue
		}
		data, err := os.ReadFile(filepath.Join("testdata", "detect", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		converted, err := Convert(context.Background(), bytes.NewReader(data), Options{})
		if err != nil {
			continue
		}
		var flagged []int
		for page, reasons := range converted.OCRReasons {
			for _, reason := range reasons {
				if reason == "suspected_garbled_text" {
					flagged = append(flagged, page)
				}
			}
		}
		sort.Ints(flagged)
		want := expectedFlaggedPages[entry.Name()]
		if !slices.Equal(flagged, want) {
			t.Errorf("%s: garbled pages %v, want %v", entry.Name(), flagged, want)
		}
		checked++
	}
	if checked < 10 {
		t.Fatalf("only %d fixtures exercised", checked)
	}
}

func TestQualityFlagsSyntheticGarbledPage(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "extract", "cid-no-tounicode.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := Convert(context.Background(), bytes.NewReader(data), Options{})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	flagged := false
	for _, reason := range result.OCRReasons[1] {
		if reason == "suspected_garbled_text" {
			flagged = true
		}
	}
	if !flagged {
		t.Errorf("synthetic unmapped-CID page must be flagged garbled, reasons=%v markdown=%q", result.OCRReasons, result.Markdown)
	}
}

func TestQualityFlagsPUAFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "extract", "pua-cmap.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := Convert(context.Background(), bytes.NewReader(data), Options{})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	reasons := result.OCRReasons[1]
	found := false
	for _, reason := range reasons {
		if reason == "suspected_garbled_text" {
			found = true
		}
	}
	if !found {
		t.Errorf("PUA-mapped page must be flagged garbled, reasons=%v", reasons)
	}
	if len(reasons) > 0 && reasons[0] != "suspected_garbled_text" {
		t.Errorf("garbled must lead the reason priority, got %v", reasons)
	}
}
