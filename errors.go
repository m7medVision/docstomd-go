package docstomd

import (
	"context"
	"errors"
	"fmt"

	"github.com/m7medVision/docstomd-go/internal/model"
	"github.com/m7medVision/docstomd-go/internal/opc"
)

type ErrorCode string

const (
	CodeUnsupported   ErrorCode = "unsupported"
	CodeNeedsOcr      ErrorCode = "needsOcr"
	CodeMalformed     ErrorCode = "malformed"
	CodeEncrypted     ErrorCode = "encrypted"
	CodeResourceLimit ErrorCode = "resourceLimit"
	CodeMissingPart   ErrorCode = "missingPart"
	CodeIO            ErrorCode = "io"
	// CodeOCRAuth: the OCR provider has no API key or rejected it.
	CodeOCRAuth ErrorCode = "ocrAuth"
	// CodeOCRRateLimited: the provider still rate-limited after retries.
	CodeOCRRateLimited ErrorCode = "ocrRateLimited"
	// CodeOCRProvider: any other OCR provider failure (server error,
	// malformed response, transport failure).
	CodeOCRProvider ErrorCode = "ocrProvider"
	// CodeCanceled: the caller's context was canceled or its deadline passed.
	CodeCanceled ErrorCode = "canceled"
)

type Error struct {
	Code      ErrorCode `json:"code"`
	Message   string    `json:"message"`
	Pages     []int     `json:"pages,omitempty"`
	PageCount int       `json:"page_count,omitempty"`
	// err is the cause, kept so errors.Is matches the provider sentinels.
	err error
}

func (e *Error) Error() string {
	if e.Code == CodeNeedsOcr && len(e.Pages) > 0 {
		return fmt.Sprintf("%s: %s (pages %v of %d)", e.Code, e.Message, e.Pages, e.PageCount)
	}
	return string(e.Code) + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.err }

func Unsupported(format string) *Error {
	return &Error{Code: CodeUnsupported, Message: "unsupported input format: " + format}
}

func Malformed(part, detail string) *Error {
	return &Error{Code: CodeMalformed, Message: part + ": " + detail}
}

func Encrypted() *Error {
	return &Error{Code: CodeEncrypted, Message: "document is encrypted"}
}

func ResourceLimit(limit, detail string) *Error {
	return &Error{Code: CodeResourceLimit, Message: limit + ": " + detail}
}

func MissingPart(part string) *Error {
	return &Error{Code: CodeMissingPart, Message: "missing required part: " + part}
}

func NeedsOcr(pages []int, pageCount int) *Error {
	return &Error{Code: CodeNeedsOcr, Message: "document contains pages that require OCR", Pages: pages, PageCount: pageCount}
}

func mapOfficeError(err error) error {
	var (
		limit     *model.LimitError
		malformed *opc.MalformedError
		missing   *opc.MissingPartError
	)
	switch {
	case errors.Is(err, opc.ErrEncrypted):
		return Encrypted()
	case errors.As(err, &limit):
		return ResourceLimit(limit.Limit, limit.Detail)
	case errors.As(err, &missing):
		return MissingPart(missing.Part)
	case errors.As(err, &malformed):
		if malformed.Part == "" {
			return Malformed("package", malformed.Detail)
		}
		return Malformed(malformed.Part, malformed.Detail)
	}
	return err
}

// mapOCRError gives a failed OCR run its stable code. ctx decides
// cancellation, so a provider's own client timeout stays a provider failure.
func mapOCRError(ctx context.Context, err error) error {
	var e *Error
	if errors.As(err, &e) {
		return err
	}
	code := CodeOCRProvider
	switch {
	case ctx.Err() != nil:
		code = CodeCanceled
	case errors.Is(err, ErrOCRMissingKey) || errors.Is(err, ErrOCRUnauthorized):
		code = CodeOCRAuth
	case errors.Is(err, ErrOCRRateLimited):
		code = CodeOCRRateLimited
	}
	return &Error{Code: code, Message: err.Error(), err: err}
}

func ErrorCodeOf(err error) ErrorCode {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return CodeCanceled
	}
	return CodeIO
}

func IsFatal(err error) bool {
	return ErrorCodeOf(err) == CodeResourceLimit
}
