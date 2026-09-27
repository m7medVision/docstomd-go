// Package ocradapter_test runs the example OCR adapters through the protocol
// conformance checks.
package ocradapter_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/m7medVision/docstomd-go"
	"github.com/m7medVision/docstomd-go/internal/ocr/external"
	"github.com/m7medVision/docstomd-go/internal/ocr/external/externaltest"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "ocr", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// build compiles an example adapter into a temp dir.
func build(t *testing.T, pkg string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), filepath.Base(pkg))
	cmd := exec.Command("go", "build", "-o", bin, "./"+pkg)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", pkg, err, out)
	}
	return bin
}

func TestGoExampleConforms(t *testing.T) {
	externaltest.Conformance(t, external.Config{Command: build(t, "template-go")}, fixture(t, "field-report-jpeg.pdf"), []int{1})
}

func TestPythonExampleConforms(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	externaltest.Conformance(t, external.Config{Command: python, Args: []string{filepath.Join("template-python", "adapter.py")}}, fixture(t, "field-report-jpeg.pdf"), []int{1})
}

func TestTesseractAdapterConforms(t *testing.T) {
	requireTesseract(t)
	externaltest.Conformance(t, external.Config{Command: build(t, "docstomd-ocr-tesseract")}, fixture(t, "field-report-jpeg.pdf"), []int{1})
}

func TestTesseractConvertsScannedFixture(t *testing.T) {
	requireTesseract(t)
	provider := docstomd.NewExternalOCRProvider(docstomd.ExternalOCRConfig{Command: build(t, "docstomd-ocr-tesseract")})
	defer func() { _ = provider.Close() }()
	for _, name := range []string{"field-report-jpeg.pdf", "field-report-flate.pdf"} {
		result, err := docstomd.Convert(context.Background(), bytes.NewReader(fixture(t, name)), docstomd.Options{OCR: docstomd.OCROptions{Mode: docstomd.OCRAuto, Provider: provider}})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, want := range []string{"# Quarterly Field Report", "42 clear days", "- Replace the wind sensor", "1,250 USD"} {
			if !strings.Contains(result.Markdown, want) {
				t.Errorf("%s: markdown lacks %q:\n%s", name, want, result.Markdown)
			}
		}
		if result.OCRCost == nil || result.OCRCost.Provider != "tesseract" || !result.OCRCost.Local {
			t.Errorf("%s: cost report = %+v", name, result.OCRCost)
		}
	}
}

func requireTesseract(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"tesseract", "pdftoppm"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
}
