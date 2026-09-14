package docstomd

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestConvertReturnsTypedUnsupported(t *testing.T) {
	res, err := Convert(context.Background(), strings.NewReader("anything"), Options{})
	if res != nil {
		t.Fatalf("result = %v, want nil", res)
	}
	if got := ErrorCodeOf(err); got != CodeUnsupported {
		t.Errorf("code = %q, want %q", got, CodeUnsupported)
	}
}

func TestConvertPropagatesRequestedFormatInError(t *testing.T) {
	_, err := Convert(context.Background(), strings.NewReader("x"), Options{Format: FormatPDF})
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("want *Error, got %T", err)
	}
	if e.Code != CodeMalformed {
		t.Errorf("code = %q, want malformed (PDF bytes required)", e.Code)
	}
}

func TestConvertHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Convert(ctx, strings.NewReader("x"), Options{})
	if err != context.Canceled {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestFormatStrings(t *testing.T) {
	cases := map[Format]string{
		FormatUnknown: "unknown",
		FormatPDF:     "pdf",
		FormatDocx:    "docx",
		FormatXlsx:    "xlsx",
		FormatPptx:    "pptx",
	}
	for f, want := range cases {
		if got := f.String(); got != want {
			t.Errorf("Format(%d).String() = %q, want %q", int(f), got, want)
		}
	}
}
