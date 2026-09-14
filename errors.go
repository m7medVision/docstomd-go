package docstomd

import (
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
)

type Error struct {
	Code      ErrorCode `json:"code"`
	Message   string    `json:"message"`
	Pages     []int     `json:"pages,omitempty"`
	PageCount int       `json:"page_count,omitempty"`
}

func (e *Error) Error() string {
	if e.Code == CodeNeedsOcr && len(e.Pages) > 0 {
		return fmt.Sprintf("%s: %s (pages %v of %d)", e.Code, e.Message, e.Pages, e.PageCount)
	}
	return string(e.Code) + ": " + e.Message
}

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

func ErrorCodeOf(err error) ErrorCode {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeIO
}

func IsFatal(err error) bool {
	return ErrorCodeOf(err) == CodeResourceLimit
}
