package docstomd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

type goldenDetection struct {
	PDFType          string          `json:"pdf_type"`
	PageCount        int             `json:"page_count"`
	PagesSampled     int             `json:"pages_sampled"`
	PagesWithText    int             `json:"pages_with_text"`
	Confidence       float64         `json:"confidence"`
	Title            *string         `json:"title"`
	OCRRecommended   bool            `json:"ocr_recommended"`
	PagesNeedingOCR  []int           `json:"pages_needing_ocr"`
	OCRReasonsByPage []goldenReasons `json:"ocr_reasons_by_page"`
}

type goldenReasons struct {
	Page    int      `json:"page"`
	Reasons []string `json:"reasons"`
}

type goldenEntry struct {
	Error string `json:"error"`
	goldenDetection
}

func TestPDFDetectionConformance(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "detect", "pdf-goldens.json"))
	if err != nil {
		t.Fatal(err)
	}
	var goldens map[string]json.RawMessage
	if err := json.Unmarshal(raw, &goldens); err != nil {
		t.Fatal(err)
	}
	if len(goldens) < 10 {
		t.Fatalf("only %d goldens, expected the full fixture corpus", len(goldens))
	}
	var totalDuration time.Duration
	for name, rawGolden := range goldens {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "detect", name))
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			det, err := Detect(context.Background(), bytes.NewReader(data), name)
			totalDuration += time.Since(start)
			var golden goldenEntry
			if err := json.Unmarshal(rawGolden, &golden); err != nil {
				t.Fatal(err)
			}
			if golden.Error != "" {
				if err == nil {
					t.Fatalf("golden expects error %q, got detection %+v", golden.Error, det)
				}
				if golden.Error == "PDF is encrypted" {
					if ErrorCodeOf(err) != CodeEncrypted {
						t.Errorf("code = %q, want encrypted (err: %v)", ErrorCodeOf(err), err)
					}
				} else if ErrorCodeOf(err) == CodeUnsupported {
					t.Errorf("unexpected unsupported for PDF input: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if det.Format != FormatPDF {
				t.Fatalf("format = %v, want pdf", det.Format)
			}
			if det.PDF == nil {
				t.Fatal("PDF payload missing")
			}
			got := det.PDF
			if string(got.Type) != golden.PDFType {
				t.Errorf("pdf_type = %q, want %q", got.Type, golden.PDFType)
			}
			if got.PageCount != golden.PageCount {
				t.Errorf("page_count = %d, want %d", got.PageCount, golden.PageCount)
			}
			if got.PagesSampled != golden.PagesSampled {
				t.Errorf("pages_sampled = %d, want %d", got.PagesSampled, golden.PagesSampled)
			}
			if got.PagesWithText != golden.PagesWithText {
				t.Errorf("pages_with_text = %d, want %d", got.PagesWithText, golden.PagesWithText)
			}
			if diff := got.Confidence - golden.Confidence; diff < -0.005 || diff > 0.005 {
				t.Errorf("confidence = %.3f, want %.3f", got.Confidence, golden.Confidence)
			}
			if got.OCRRecommended != golden.OCRRecommended {
				t.Errorf("ocr_recommended = %v, want %v", got.OCRRecommended, golden.OCRRecommended)
			}
			if !intSlicesEqual(got.PagesNeedingOCR, golden.PagesNeedingOCR) {
				t.Errorf("pages_needing_ocr = %v, want %v", got.PagesNeedingOCR, golden.PagesNeedingOCR)
			}
			if len(got.OCRReasonsByPage) != len(golden.OCRReasonsByPage) {
				t.Errorf("ocr_reasons_by_page: got %d entries, want %d", len(got.OCRReasonsByPage), len(golden.OCRReasonsByPage))
			} else {
				for i, want := range golden.OCRReasonsByPage {
					g := got.OCRReasonsByPage[i]
					if g.Page != want.Page || !stringSlicesEqual(g.Reasons, want.Reasons) {
						t.Errorf("reasons[%d] = {page %d, %v}, want {page %d, %v}", i, g.Page, g.Reasons, want.Page, want.Reasons)
					}
				}
			}
			if (got.Title == nil) != (golden.Title == nil) {
				t.Errorf("title = %v, want %v", got.Title, golden.Title)
			} else if got.Title != nil && *got.Title != *golden.Title {
				t.Errorf("title = %q, want %q", *got.Title, *golden.Title)
			}
		})
	}
	if avg := totalDuration / time.Duration(len(goldens)); avg > 100*time.Millisecond {
		t.Errorf("average classification time %v exceeds the tens-of-milliseconds budget", avg)
	}
}

func TestPDFDetectionNotAPDFStaysUnknown(t *testing.T) {
	det, err := Detect(context.Background(), bytes.NewReader([]byte("plain text")), "")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if det.Format != FormatUnknown {
		t.Errorf("format = %v, want unknown", det.Format)
	}
	if det.PDF != nil {
		t.Error("non-PDF input must not carry a PDF payload")
	}
}

func intSlicesEqual(a, b []int) bool {
	return slices.Equal(a, b)
}

func stringSlicesEqual(a, b []string) bool {
	return slices.Equal(a, b)
}
