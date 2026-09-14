package docstomd

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"

	"github.com/m7medVision/docstomd-go/internal/model"
	"github.com/m7medVision/docstomd-go/internal/opc"
)

func encryptedOOXML() []byte {
	data := make([]byte, 1024)
	copy(data, oleHeader)
	entry := data[512+128:]
	units := utf16.Encode([]rune("EncryptedPackage"))
	for i, u := range units {
		binary.LittleEndian.PutUint16(entry[2*i:], u)
	}
	binary.LittleEndian.PutUint16(entry[64:], uint16(2*(len(units)+1)))
	entry[66] = 2
	return data
}

func TestConvertEncryptedOOXMLIsEncrypted(t *testing.T) {
	for _, f := range []Format{FormatUnknown, FormatDocx, FormatXlsx, FormatPptx} {
		_, err := Convert(context.Background(), bytes.NewReader(encryptedOOXML()), Options{Format: f})
		if got := ErrorCodeOf(err); got != CodeEncrypted {
			t.Errorf("format %v: code = %q, want %q (%v)", f, got, CodeEncrypted, err)
		}
	}
}

func TestMapOfficeError(t *testing.T) {
	cases := []struct {
		err  error
		want ErrorCode
	}{
		{opc.ErrEncrypted, CodeEncrypted},
		{&opc.MalformedError{Part: "word/document.xml", Detail: "no body"}, CodeMalformed},
		{&opc.MissingPartError{Part: "xl/workbook.xml"}, CodeMissingPart},
		{fmt.Errorf("sheet 2: %w", &model.LimitError{Limit: "max_expansion", Detail: "too wide"}), CodeResourceLimit},
	}
	for _, c := range cases {
		if got := ErrorCodeOf(mapOfficeError(c.err)); got != c.want {
			t.Errorf("mapOfficeError(%v) code = %q, want %q", c.err, got, c.want)
		}
	}
	if mapOfficeError(nil) != nil {
		t.Error("nil stays nil")
	}
}

func TestConvertBrokenOfficePackageUsesTheNameHint(t *testing.T) {
	truncated := []byte("PK\x03\x04truncated central directory")
	for _, name := range []string{"report.docx", "sheet.xlsx", "deck.pptx"} {
		for _, data := range [][]byte{nil, truncated} {
			_, err := Convert(context.Background(), bytes.NewReader(data), Options{FileName: name})
			if got := ErrorCodeOf(err); got != CodeMalformed {
				t.Errorf("%s (%d bytes): code = %q, want malformed (%v)", name, len(data), got, err)
			}
			_, err = Convert(context.Background(), bytes.NewReader(data), Options{})
			if got := ErrorCodeOf(err); got != CodeUnsupported {
				t.Errorf("%s (%d bytes) without a name: code = %q, want unsupported", name, len(data), got)
			}
		}
	}
	data, err := os.ReadFile(filepath.Join("testdata", "detect", "cropbox_offset_origin.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := Convert(context.Background(), bytes.NewReader(data), Options{FileName: "mislabeled.docx"})
	if err != nil || result.Format != FormatPDF {
		t.Fatalf("content wins over the name hint: %v, %v", result, err)
	}
}
