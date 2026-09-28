package backend

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/m7medVision/docstomd-go/ocr/catalog"
)

func TestAutoFallsBackToGoWithHint(t *testing.T) {
	t.Setenv(catalog.EnvRuntimes, t.TempDir())
	sel, err := New("auto")
	if err != nil {
		t.Fatal(err)
	}
	if sel.Name != "go" {
		t.Errorf("auto without a runtime chose %s", sel.Name)
	}
	if _, err := catalog.RuntimeFor("onnx"); err == nil && !strings.Contains(sel.Hint, "docstomd ocr install --backend onnx") {
		t.Errorf("hint = %q", sel.Hint)
	}
	if _, err := New("onnx"); !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "install --backend onnx") {
		t.Errorf("onnx without a runtime: %v", err)
	}
	if _, err := New("tpu"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("unknown backend: %v", err)
	}
}

// TestAutoPrefersInstalledRuntime runs when DOCSTOMD_OCR_RUNTIMES points at
// an installed ONNX Runtime.
func TestAutoPrefersInstalledRuntime(t *testing.T) {
	rt, err := catalog.RuntimeFor("onnx")
	if err != nil || os.Getenv(catalog.EnvRuntimes) == "" || !rt.Installed() {
		t.Skip("no installed ONNX Runtime (set DOCSTOMD_OCR_RUNTIMES)")
	}
	sel, err := New("auto")
	if err != nil {
		t.Fatal(err)
	}
	if sel.Name != "onnx" || sel.Hint != "" {
		t.Errorf("auto chose %s (hint %q), want onnx", sel.Name, sel.Hint)
	}
}
