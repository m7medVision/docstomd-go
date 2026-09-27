package docstomd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestErrorCodes(t *testing.T) {
	cases := []struct {
		err  error
		want ErrorCode
	}{
		{Unsupported("pdf"), CodeUnsupported},
		{Malformed("xref", "truncated"), CodeMalformed},
		{Encrypted(), CodeEncrypted},
		{ResourceLimit("max-entry-bytes", "entry exceeds 128 MiB"), CodeResourceLimit},
		{MissingPart("word/document.xml"), CodeMissingPart},
		{NeedsOcr([]int{2, 3}, 10), CodeNeedsOcr},
	}
	for _, tc := range cases {
		if got := ErrorCodeOf(tc.err); got != tc.want {
			t.Errorf("ErrorCodeOf(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestNeedsOcrCarriesPages(t *testing.T) {
	err := NeedsOcr([]int{2, 3}, 10)
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("NeedsOcr does not produce *Error, got %T", err)
	}
	if len(e.Pages) != 2 || e.Pages[0] != 2 || e.Pages[1] != 3 {
		t.Errorf("pages = %v, want [2 3]", e.Pages)
	}
	if e.PageCount != 10 {
		t.Errorf("page count = %d, want 10", e.PageCount)
	}
}

func TestErrorCodeOfUnwrapsWrappedErrors(t *testing.T) {
	wrapped := fmt.Errorf("reading page tree: %w", Encrypted())
	if got := ErrorCodeOf(wrapped); got != CodeEncrypted {
		t.Errorf("ErrorCodeOf(wrapped) = %q, want %q", got, CodeEncrypted)
	}
}

func TestErrorCodeOfPlainIOError(t *testing.T) {
	if got := ErrorCodeOf(io.ErrUnexpectedEOF); got != CodeIO {
		t.Errorf("ErrorCodeOf(io error) = %q, want %q", got, CodeIO)
	}
}

func TestOnlyResourceLimitIsFatal(t *testing.T) {
	if !IsFatal(ResourceLimit("max-xml-nodes", "exceeded")) {
		t.Error("resourceLimit must be fatal")
	}
	for _, err := range []error{Malformed("xref", "bad"), Encrypted(), Unsupported("pdf"), io.EOF} {
		if IsFatal(err) {
			t.Errorf("IsFatal(%v) = true, want false", err)
		}
	}
}

func TestMapOCRError(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	live := context.Background()
	cases := []struct {
		name     string
		ctx      context.Context
		err      error
		want     ErrorCode
		sentinel error
	}{
		{"missing key", live, ErrOCRMissingKey, CodeOCRAuth, ErrOCRMissingKey},
		{"unauthorized", live, fmt.Errorf("%w (HTTP 401)", ErrOCRUnauthorized), CodeOCRAuth, ErrOCRUnauthorized},
		{"rate limited", live, fmt.Errorf("%w after 4 attempts", ErrOCRRateLimited), CodeOCRRateLimited, ErrOCRRateLimited},
		{"server error", live, errors.New("mistral: HTTP 503"), CodeOCRProvider, nil},
		{"caller canceled", canceled, context.Canceled, CodeCanceled, context.Canceled},
		{"typed error kept", live, Encrypted(), CodeEncrypted, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapOCRError(tc.ctx, tc.err)
			if code := ErrorCodeOf(got); code != tc.want {
				t.Errorf("code = %q, want %q", code, tc.want)
			}
			if tc.sentinel != nil && !errors.Is(got, tc.sentinel) {
				t.Errorf("errors.Is(%v, %v) = false", got, tc.sentinel)
			}
			if !strings.Contains(got.Error(), tc.err.Error()) {
				t.Errorf("message %q lost cause %q", got.Error(), tc.err.Error())
			}
		})
	}
}

func TestErrorCodeOfContextErrors(t *testing.T) {
	for _, err := range []error{context.Canceled, fmt.Errorf("reading: %w", context.DeadlineExceeded)} {
		if got := ErrorCodeOf(err); got != CodeCanceled {
			t.Errorf("ErrorCodeOf(%v) = %q, want %q", err, got, CodeCanceled)
		}
	}
}
