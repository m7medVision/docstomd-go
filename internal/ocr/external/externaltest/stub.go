// Package externaltest is a stub OCR engine for protocol tests: a test
// binary re-executes itself with EnvMode set and calls Stub.
package externaltest

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/m7medVision/docstomd-go/internal/ocr"
	"github.com/m7medVision/docstomd-go/internal/ocr/external"
)

// EnvMode selects the stub's behaviour; a test binary that finds it set runs
// Stub instead of its tests.
const EnvMode = "DOCSTOMD_STUB"

// EnvStarts names, when set, a file the stub appends "start\n" to each time
// it starts, so tests can count engine processes.
const EnvStarts = "DOCSTOMD_STUB_STARTS"

// Stub speaks the OCR protocol on stdin/stdout in the named mode: markdown,
// lines, single (answer once, exit), or a failure mode (nohello, badhello,
// version2, malformed, extrapage, empty, wrongid, unavailable, failed, crash,
// hang). It returns the process exit code.
func Stub(mode string) int {
	if path := os.Getenv(EnvStarts); path != "" {
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			_, _ = f.WriteString("start\n")
			_ = f.Close()
		}
	}
	out := json.NewEncoder(os.Stdout)
	switch mode {
	case "nohello":
		fmt.Fprintln(os.Stderr, "model file missing")
		return 1
	case "badhello":
		fmt.Println("hello there")
		return 0
	case "version2":
		_ = out.Encode(external.Hello{Type: "hello", Protocol: 2, Engine: "stub"})
	default:
		_ = out.Encode(external.Hello{Type: "hello", Protocol: 1, Engine: "stub", Version: "1.0", PageCost: 0.5, Local: mode == "lines"})
	}
	in := bufio.NewReader(os.Stdin)
	for {
		line, err := in.ReadBytes('\n')
		if err != nil {
			return 0
		}
		var req external.Request
		if err := json.Unmarshal(line, &req); err != nil {
			return 2
		}
		pdf, _ := base64.StdEncoding.DecodeString(req.PDF)
		resp := external.Response{Type: "result", ID: req.ID}
		for _, page := range req.Pages {
			switch mode {
			case "lines":
				resp.Pages = append(resp.Pages, external.PageResult{Page: page, Width: 1000, Height: 1000, Lines: []ocr.Line{{Text: fmt.Sprintf("line on page %d of %s", page, pdf), Box: ocr.Rect{X0: 100, Y0: 100, X1: 600, Y1: 130}, Confidence: 0.9}}})
			default:
				md := fmt.Sprintf("page %d of %s", page, pdf)
				resp.Pages = append(resp.Pages, external.PageResult{Page: page, Markdown: &md})
			}
		}
		switch mode {
		case "malformed":
			fmt.Println("{not json")
			continue
		case "extrapage":
			md := "x"
			resp.Pages = append(resp.Pages, external.PageResult{Page: 99, Markdown: &md})
		case "empty":
			resp.Pages = []external.PageResult{{Page: req.Pages[0]}}
		case "wrongid":
			resp.ID = "nope"
		case "unavailable":
			resp = external.Response{Type: "error", ID: req.ID, Code: external.CodeUnavailable, Message: "model pp-ocrv5 not installed"}
		case "failed":
			resp = external.Response{Type: "error", ID: req.ID, Code: external.CodeFailed, Message: "out of memory"}
		case "crash":
			fmt.Fprintln(os.Stderr, "segfault in detector")
			return 3
		case "hang":
			time.Sleep(time.Minute)
		}
		_ = out.Encode(resp)
		if mode == "single" {
			return 0
		}
	}
}
