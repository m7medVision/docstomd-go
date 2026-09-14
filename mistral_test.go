package docstomd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type mistralFixtureTransport struct {
	t        *testing.T
	fixture  string
	requests []string
}

func (f *mistralFixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	f.requests = append(f.requests, string(body))
	data, err := os.ReadFile(filepath.Join("internal", "ocr", "mistral", "testdata", f.fixture))
	if err != nil {
		f.t.Fatal(err)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(data)), Request: req}, nil
}

func recordedMistral(t *testing.T) (OCRProvider, *mistralFixtureTransport) {
	t.Helper()
	t.Setenv("MISTRAL_API_KEY", "sk-fixture")
	transport := &mistralFixtureTransport{t: t, fixture: "success.json"}
	return NewMistralProvider(MistralOptions{HTTPClient: &http.Client{Transport: transport}}), transport
}

func TestMistralAutoReplacesScannedPage(t *testing.T) {
	provider, transport := recordedMistral(t)
	data, err := os.ReadFile(filepath.Join("testdata", "detect", "scan_with_native_header_text.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := Convert(context.Background(), bytes.NewReader(data), Options{OCR: OCROptions{Mode: OCRAuto, Provider: provider}})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if len(transport.requests) != 1 || !strings.Contains(transport.requests[0], `"pages":[0]`) {
		t.Errorf("requests = %d, want one batched call for page index 0", len(transport.requests))
	}
	if !strings.HasPrefix(result.Markdown, "# Order Detail Report by Account") {
		t.Errorf("markdown = %.80q, want recorded OCR page", result.Markdown)
	}
	if result.OCRCost == nil || result.OCRCost.Provider != "mistral" || result.OCRCost.PagesBilled != 1 || result.OCRCost.EstimatedCostUSD != 0.004 {
		t.Errorf("cost = %+v, want 1 mistral page at 0.004", result.OCRCost)
	}
	if len(result.NeedsReview) != 0 {
		t.Errorf("needs review = %v, want none", result.NeedsReview)
	}
}

func TestMistralHonorsCaps(t *testing.T) {
	provider, transport := recordedMistral(t)
	result, err := Convert(context.Background(), reader(scannedFixture(t)), Options{OCR: OCROptions{Mode: OCRAuto, Provider: provider, MaxPagesPerDoc: 1}})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if len(transport.requests) != 1 || !strings.Contains(transport.requests[0], `"pages":[0]`) {
		t.Errorf("requests = %v, want one call billing only page index 0", len(transport.requests))
	}
	if result.OCRCost == nil || !result.OCRCost.Truncated || fmt.Sprint(result.OCRCost.BilledPages) != "[1]" {
		t.Errorf("cost = %+v, want truncated bill of [1]", result.OCRCost)
	}
	if fmt.Sprint(result.NeedsReview) != "[2]" {
		t.Errorf("needs review = %v, want [2]", result.NeedsReview)
	}
}

func TestMistralDryRunNeverCallsAPI(t *testing.T) {
	provider, transport := recordedMistral(t)
	result, err := Convert(context.Background(), reader(scannedFixture(t)), Options{OCR: OCROptions{Mode: OCRAuto, Provider: provider, DryRun: true}})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if len(transport.requests) != 0 {
		t.Fatalf("dry-run made %d API calls", len(transport.requests))
	}
	if result.OCRCost == nil || !result.OCRCost.DryRun || result.OCRCost.EstimatedCostUSD != 0.008 {
		t.Errorf("cost = %+v, want dry-run estimate 0.008", result.OCRCost)
	}
}

func TestDefaultProviderIsMistral(t *testing.T) {
	t.Setenv("MISTRAL_API_KEY", "")
	result, err := Convert(context.Background(), reader(scannedFixture(t)), Options{OCR: OCROptions{Mode: OCRAuto, DryRun: true}})
	if err != nil {
		t.Fatalf("dry-run without key: %v", err)
	}
	if result.OCRCost == nil || result.OCRCost.Provider != "mistral" || result.OCRCost.PagesBilled != 2 {
		t.Errorf("cost = %+v, want mistral estimate for 2 pages", result.OCRCost)
	}
}

func TestMissingMistralKeyIsTyped(t *testing.T) {
	t.Setenv("MISTRAL_API_KEY", "")
	_, err := Convert(context.Background(), reader(scannedFixture(t)), Options{OCR: OCROptions{Mode: OCRAuto}})
	if !errors.Is(err, ErrOCRMissingKey) {
		t.Fatalf("err = %v, want ErrOCRMissingKey", err)
	}
	if !strings.Contains(err.Error(), "MISTRAL_API_KEY") {
		t.Errorf("err = %v, want actionable env var hint", err)
	}
}
