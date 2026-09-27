package docstomd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/m7medVision/docstomd-go/internal/pdftest"
)

func TestConvertHostilePDFIsMalformed(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"page tree cycle", pdftest.PageTreeCycle()},
		{"object stream inside itself", pdftest.ObjectStreamCycle(10)},
		{"object streams inside each other", pdftest.ObjectStreamCycle(11)},
		{"deep arrays", pdftest.DeepNesting(100_000, "[")},
		{"deep dictionaries", pdftest.DeepNesting(100_000, "<<")},
		{"xref predictor negative colors", pdftest.XRefPredictor(-8)},
		{"xref W negative", pdftest.XRefWidths("[1 -2 1]")},
		{"xref offset negative", pdftest.NegativeXRefOffset()},
		{"object stream negative N", pdftest.ObjectStreamCount("-1")},
		{"object stream huge N", pdftest.ObjectStreamCount("1099511627776")},
		{"page number +Inf", pdftest.NonFiniteNumber("+Inf")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Convert(context.Background(), bytes.NewReader(tt.data), Options{})
			if got := ErrorCodeOf(err); got != CodeMalformed {
				t.Errorf("code = %q (%v), want %q", got, err, CodeMalformed)
			}
		})
	}
}

// Interpreter caps cut hostile content short without failing the document.
func TestConvertHostileContentKeepsText(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{"form chain", pdftest.FormChain(100), "form1"},
		{"q nesting", pdftest.QNesting(100_000), "deep"},
		{"xref stream without type field", pdftest.XRefWithoutType(), "typeless"},
		{"flate bomb", pdftest.FlateBomb(20 << 20), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Convert(context.Background(), bytes.NewReader(tt.data), Options{})
			if err != nil {
				if tt.want == "" && ErrorCodeOf(err) == CodeNeedsOcr {
					return
				}
				t.Fatalf("Convert: %v", err)
			}
			if !strings.Contains(result.Markdown, tt.want) {
				t.Errorf("markdown = %q, want it to contain %q", result.Markdown, tt.want)
			}
		})
	}
}
