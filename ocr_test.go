package docstomd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	pdfocr "github.com/m7medVision/docstomd-go/internal/ocr"
)

type pdfocrPageResult = pdfocr.PageResult

// fakeOCRProvider records calls and returns scripted per-page markdown.
type fakeOCRProvider struct {
	name        string
	costPerPage float64
	calls       [][]int
	pages       map[int]string
	confidence  map[int]float64
}

func (f *fakeOCRProvider) Name() string         { return f.name }
func (f *fakeOCRProvider) EstPageCost() float64 { return f.costPerPage }

func (f *fakeOCRProvider) Recognize(ctx context.Context, doc OCRDocument, pages []int) ([]pdfocrPageResult, error) {
	recorded := append([]int{}, pages...)
	f.calls = append(f.calls, recorded)
	out := []pdfocrPageResult{}
	for _, p := range pages {
		md := f.pages[p]
		conf := 0.9
		if c, ok := f.confidence[p]; ok {
			conf = c
		}
		out = append(out, pdfocrPageResult{Page: p, Markdown: md, Confidence: conf})
	}
	return out, nil
}

func scannedFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "detect", "handmade-scanned.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestOCRAutoSendsExactlyRoutedPagesInOneCall(t *testing.T) {
	fake := &fakeOCRProvider{name: "fake", costPerPage: 0.001, pages: map[int]string{1: "# Page One", 2: "Page two body"}}
	result, err := Convert(context.Background(), reader(scannedFixture(t)), Options{OCR: OCROptions{Mode: OCRAuto, Provider: fake}})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("provider calls = %d, want exactly 1 batched call", len(fake.calls))
	}
	if fmt.Sprint(fake.calls[0]) != "[1 2]" {
		t.Errorf("billed pages = %v, want [1 2]", fake.calls[0])
	}
	if !strings.Contains(result.Markdown, "# Page One") || !strings.Contains(result.Markdown, "Page two body") {
		t.Errorf("healthy OCR must replace scanned pages, got %q", result.Markdown)
	}
	if result.OCRCost == nil || result.OCRCost.PagesBilled != 2 || result.OCRCost.Provider != "fake" {
		t.Errorf("cost report = %+v, want 2 billed by fake", result.OCRCost)
	}
	if fmt.Sprint(result.OCRCost.BilledPages) != "[1 2]" {
		t.Errorf("billed page list = %v, want [1 2]", result.OCRCost.BilledPages)
	}
	if result.OCRCost.EstimatedCostUSD != 0.002 {
		t.Errorf("estimated cost = %v, want 0.002", result.OCRCost.EstimatedCostUSD)
	}
}

func TestOCRForceSendsAllPages(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "detect", "cropbox_offset_origin.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeOCRProvider{name: "fake", costPerPage: 0.001, pages: map[int]string{1: "# Forced"}}
	result, err := Convert(context.Background(), reader(data), Options{OCR: OCROptions{Mode: OCRForce, Provider: fake}})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if len(fake.calls) != 1 || len(fake.calls[0]) != result.PageCount {
		t.Errorf("force must bill every page once, calls=%v count=%d", fake.calls, result.PageCount)
	}
	if !strings.Contains(result.Markdown, "# Forced") {
		t.Errorf("forced OCR must replace page 1, got %q", result.Markdown)
	}
}

func TestOCROffStillErrorsForScanned(t *testing.T) {
	_, err := Convert(context.Background(), reader(scannedFixture(t)), Options{})
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != CodeNeedsOcr {
		t.Fatalf("want typed needsOcr, got %v", err)
	}
}

func TestOCRCapsTruncate(t *testing.T) {
	fake := &fakeOCRProvider{name: "fake", costPerPage: 0.001, pages: map[int]string{1: "# Only One"}}
	result, err := Convert(context.Background(), reader(scannedFixture(t)), Options{OCR: OCROptions{Mode: OCRAuto, Provider: fake, MaxPagesPerDoc: 1}})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if len(fake.calls) != 1 || fmt.Sprint(fake.calls[0]) != "[1]" {
		t.Errorf("capped pages = %v, want [1]", fake.calls)
	}
	if result.OCRCost == nil || !result.OCRCost.Truncated || result.OCRCost.PagesBilled != 1 {
		t.Errorf("cost = %+v, want truncated 1-page bill", result.OCRCost)
	}
	if len(result.NeedsReview) != 1 || result.NeedsReview[0] != 2 {
		t.Errorf("needs review = %v, want [2] (page beyond cap keeps native empty)", result.NeedsReview)
	}
}

func TestOCRRunCapSpansConvertCalls(t *testing.T) {
	run := &OCRRun{}
	convert := func() (*fakeOCRProvider, *Result) {
		t.Helper()
		fake := &fakeOCRProvider{name: "fake", costPerPage: 0.001, pages: map[int]string{1: "# One", 2: "Two"}}
		result, err := Convert(context.Background(), reader(scannedFixture(t)), Options{OCR: OCROptions{Mode: OCRAuto, Provider: fake, MaxPagesPerRun: 3, Run: run}})
		if err != nil {
			t.Fatalf("Convert: %v", err)
		}
		return fake, result
	}
	first, _ := convert()
	second, result := convert()
	if fmt.Sprint(first.calls) != "[[1 2]]" || fmt.Sprint(second.calls) != "[[1]]" {
		t.Fatalf("calls sharing a run = %v then %v, want [[1 2]] then [[1]]", first.calls, second.calls)
	}
	if !result.OCRCost.Truncated || fmt.Sprint(result.NeedsReview) != "[2]" {
		t.Errorf("second call cost %+v, needs review %v", result.OCRCost, result.NeedsReview)
	}
	third, result := convert()
	if len(third.calls) != 0 || result.OCRCost.PagesBilled != 0 || !result.OCRCost.Truncated {
		t.Errorf("an exhausted run must not call the provider: calls %v, cost %+v", third.calls, result.OCRCost)
	}

	unshared := &fakeOCRProvider{name: "fake", pages: map[int]string{1: "# One", 2: "Two"}}
	for range 2 {
		if _, err := Convert(context.Background(), reader(scannedFixture(t)), Options{OCR: OCROptions{Mode: OCRAuto, Provider: unshared, MaxPagesPerRun: 3}}); err != nil {
			t.Fatal(err)
		}
	}
	if fmt.Sprint(unshared.calls) != "[[1 2] [1 2]]" {
		t.Errorf("without a shared run each call has its own budget, calls %v", unshared.calls)
	}
}

func TestOCRRunIsSafeForConcurrentConverts(t *testing.T) {
	run := &OCRRun{}
	data := scannedFixture(t)
	billed := make(chan int, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			fake := &fakeOCRProvider{name: "fake", pages: map[int]string{1: "# One", 2: "Two"}}
			result, err := Convert(context.Background(), reader(data), Options{OCR: OCROptions{Mode: OCRAuto, Provider: fake, MaxPagesPerRun: 5, Run: run}})
			if err != nil {
				t.Error(err)
				return
			}
			billed <- result.OCRCost.PagesBilled
		})
	}
	wg.Wait()
	close(billed)
	total := 0
	for n := range billed {
		total += n
	}
	if total != 5 {
		t.Fatalf("concurrent calls billed %d pages under a 5-page run cap", total)
	}
}

func TestOCRDryRunNeverCallsProvider(t *testing.T) {
	fake := &fakeOCRProvider{name: "fake", costPerPage: 0.0015, pages: map[int]string{1: "should not appear"}}
	result, err := Convert(context.Background(), reader(scannedFixture(t)), Options{OCR: OCROptions{Mode: OCRAuto, Provider: fake, DryRun: true}})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("dry-run must not call the provider, calls=%v", fake.calls)
	}
	if result.OCRCost == nil || !result.OCRCost.DryRun || result.OCRCost.PagesBilled != 2 || result.OCRCost.EstimatedCostUSD != 0.003 {
		t.Errorf("dry-run cost = %+v, want 2 pages at 0.003", result.OCRCost)
	}
	if strings.Contains(result.Markdown, "should not appear") {
		t.Error("dry-run must not inject OCR content")
	}
}

func TestOCRFusionWeakAndGarbledKeepsNative(t *testing.T) {
	shifted := shiftForTest("the quick brown fox jumps over the lazy dog again and again until the sample of letters is large enough for the statistics to become reliable and stable across the whole histogram analysis ")
	base := shifted
	for len(shifted) < 2200 {
		shifted += base
	}
	data, err := os.ReadFile(filepath.Join("testdata", "detect", "cropbox_offset_origin.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeOCRProvider{
		name:        "fake",
		costPerPage: 0.001,
		pages:       map[int]string{1: ""},
		confidence:  map[int]float64{},
	}
	result, err := Convert(context.Background(), reader(data), Options{OCR: OCROptions{Mode: OCRForce, Provider: fake}})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if !strings.Contains(result.Markdown, "Visible glyph") {
		t.Errorf("empty OCR must keep native text, got %q", result.Markdown)
	}
	if len(result.NeedsReview) != 1 || result.NeedsReview[0] != 1 {
		t.Errorf("needs review = %v, want [1]", result.NeedsReview)
	}

	fake.pages = map[int]string{1: shifted[:2200]}
	result, err = Convert(context.Background(), reader(data), Options{OCR: OCROptions{Mode: OCRForce, Provider: fake}})
	if err != nil {
		t.Fatalf("Convert garbled: %v", err)
	}
	if !strings.Contains(result.Markdown, "Visible glyph") {
		t.Errorf("garbled OCR must keep native text, got %.60q", result.Markdown)
	}
	if len(result.NeedsReview) != 1 {
		t.Errorf("garbled OCR page must be flagged needs review, got %v", result.NeedsReview)
	}

	fake.pages = map[int]string{1: "# Healthy OCR"}
	fake.confidence = map[int]float64{1: 0.2}
	result, err = Convert(context.Background(), reader(data), Options{OCR: OCROptions{Mode: OCRForce, Provider: fake}})
	if err != nil {
		t.Fatalf("Convert low-conf: %v", err)
	}
	if !strings.Contains(result.Markdown, "Visible glyph") || len(result.NeedsReview) != 1 {
		t.Errorf("low-confidence OCR must keep native and flag review, md=%.40q review=%v", result.Markdown, result.NeedsReview)
	}
}

func TestOCRFusionAcceptsSpacedDotLeaders(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "detect", "cropbox_offset_origin.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	toc := "# Contents\n\nAuthor's Note . . . . . . . . . . . . . . . . . . . . . . . . . . . . . ix\n" +
		"Foreword . . . . . . . . . . . . . . . . . . . . . . . . . . . . . . . . . . . . xi\n" +
		"1. A Fountain in the Square . . . . . . . . . . . . . . . . . . . . . . . . . 1\n"
	fake := &fakeOCRProvider{name: "fake", costPerPage: 0.001, pages: map[int]string{1: toc}}
	result, err := Convert(context.Background(), reader(data), Options{OCR: OCROptions{Mode: OCRForce, Provider: fake}})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if len(result.NeedsReview) != 0 || !strings.Contains(result.Markdown, "Foreword . . .") {
		t.Errorf("healthy contents-page OCR must replace the page, review=%v md=%.60q", result.NeedsReview, result.Markdown)
	}
}

func reader(b []byte) *strings.Reader { return strings.NewReader(string(b)) }

func shiftForTest(s string) string {
	out := []rune(s)
	for i, r := range out {
		if r >= 'a' && r <= 'z' {
			out[i] = rune('a' + (int(r-'a')+2)%26)
		}
	}
	return string(out)
}
