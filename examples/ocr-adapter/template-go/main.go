// Command template-go is a minimal docstomd OCR adapter in Go: copy it,
// replace recognize with a call to your engine or vendor API, and run
//
//	docstomd convert --ocr auto --ocr-provider exec:./template-go scan.pdf
//
// The protocol is documented in docs/ocr-protocol.md. This file uses only
// the standard library, so it has no dependency on docstomd.
package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
)

type request struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	PDF      string `json:"pdf"`
	Pages    []int  `json:"pages"`
	Password string `json:"password,omitempty"`
}

type box struct {
	X0 float64 `json:"x0"`
	Y0 float64 `json:"y0"`
	X1 float64 `json:"x1"`
	Y1 float64 `json:"y1"`
}

type line struct {
	Text       string  `json:"text"`
	Box        box     `json:"box"`
	Confidence float64 `json:"confidence,omitempty"`
}

// page answers one page with Markdown or with lines; set one of them.
type page struct {
	Page       int      `json:"page"`
	Markdown   *string  `json:"markdown,omitempty"`
	Lines      []line   `json:"lines,omitempty"`
	Width      float64  `json:"width,omitempty"`
	Height     float64  `json:"height,omitempty"`
	Confidence *float64 `json:"confidence,omitempty"`
}

// recognize is where your engine goes. This placeholder answers with one
// line per page in a 1000×1000 coordinate space.
func recognize(pdf []byte, pageNum int) (page, error) {
	text := fmt.Sprintf("Page %d of a %d-byte PDF", pageNum, len(pdf))
	return page{Page: pageNum, Width: 1000, Height: 1000, Lines: []line{
		{Text: text, Box: box{X0: 100, Y0: 100, X1: 900, Y1: 130}, Confidence: 0.99},
	}}, nil
}

func main() {
	out := json.NewEncoder(os.Stdout) // one JSON object per line
	out.Encode(map[string]any{"type": "hello", "protocol": 1, "engine": "example-go", "version": "1.0.0", "local": true})
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(nil, 512<<20) // requests carry the whole PDF
	for in.Scan() {
		var req request
		if err := json.Unmarshal(in.Bytes(), &req); err != nil {
			fmt.Fprintln(os.Stderr, "bad request:", err)
			os.Exit(2)
		}
		pdf, err := base64.StdEncoding.DecodeString(req.PDF)
		if err != nil {
			out.Encode(map[string]any{"type": "error", "id": req.ID, "code": "failed", "message": err.Error()})
			continue
		}
		pages := []page{}
		for _, n := range req.Pages {
			p, err := recognize(pdf, n)
			if err != nil {
				fmt.Fprintf(os.Stderr, "page %d: %v\n", n, err)
				continue // an unanswered page keeps its native text
			}
			pages = append(pages, p)
		}
		out.Encode(map[string]any{"type": "result", "id": req.ID, "pages": pages})
	}
	// stdin closed: the session is over.
}
