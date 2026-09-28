// Command docstomd-ocr-tesseract is a docstomd OCR adapter for the Tesseract
// CLI. It renders each requested page with pdftoppm (poppler), runs
// tesseract on it and answers with text lines and their boxes.
//
//	docstomd convert --ocr auto --ocr-provider exec:docstomd-ocr-tesseract scan.pdf
//
// Flags: --lang (tesseract languages, default eng), --dpi (default 300).
// It needs tesseract and pdftoppm on PATH; without them it answers every
// request with an "unavailable" error.
package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type rect struct {
	X0 float64 `json:"x0"`
	Y0 float64 `json:"y0"`
	X1 float64 `json:"x1"`
	Y1 float64 `json:"y1"`
}

type line struct {
	Text       string  `json:"text"`
	Box        rect    `json:"box"`
	Confidence float64 `json:"confidence,omitempty"`
}

type page struct {
	Page   int     `json:"page"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Lines  []line  `json:"lines"`
}

type request struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	PDF      string `json:"pdf"`
	Pages    []int  `json:"pages"`
	Password string `json:"password"`
}

// errUnavailable marks failures the user fixes by installing something.
var errUnavailable = errors.New("unavailable")

func main() {
	lang := flag.String("lang", "eng", "tesseract languages, e.g. eng+deu")
	dpi := flag.Int("dpi", 300, "render resolution")
	flag.Parse()

	enc := json.NewEncoder(os.Stdout)
	send := func(msg map[string]any) {
		if err := enc.Encode(msg); err != nil {
			os.Exit(1) // docstomd went away
		}
	}
	send(map[string]any{"type": "hello", "protocol": 1, "engine": "tesseract", "version": tesseractVersion(), "local": true})
	in := bufio.NewReader(os.Stdin)
	for {
		msg, err := in.ReadBytes('\n')
		if len(bytes.TrimSpace(msg)) == 0 {
			if err != nil {
				return
			}
			continue
		}
		var req request
		if err := json.Unmarshal(msg, &req); err != nil {
			fmt.Fprintln(os.Stderr, "bad request:", err)
			os.Exit(2)
		}
		pages, err := recognize(req, *lang, *dpi)
		switch {
		case errors.Is(err, errUnavailable):
			send(map[string]any{"type": "error", "id": req.ID, "code": "unavailable", "message": err.Error()})
		case err != nil:
			send(map[string]any{"type": "error", "id": req.ID, "code": "failed", "message": err.Error()})
		default:
			send(map[string]any{"type": "result", "id": req.ID, "pages": pages})
		}
	}
}

func tesseractVersion() string {
	out, err := exec.Command("tesseract", "--version").Output()
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(string(out), "\n")
	return strings.TrimSpace(strings.TrimPrefix(first, "tesseract"))
}

func recognize(req request, lang string, dpi int) ([]page, error) {
	for _, tool := range []string{"pdftoppm", "tesseract"} {
		if _, err := exec.LookPath(tool); err != nil {
			return nil, fmt.Errorf("%w: %s is not installed (tesseract adapter needs tesseract and poppler's pdftoppm)", errUnavailable, tool)
		}
	}
	pdf, err := base64.StdEncoding.DecodeString(req.PDF)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "docstomd-tesseract")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	input := filepath.Join(dir, "in.pdf")
	if err := os.WriteFile(input, pdf, 0o600); err != nil {
		return nil, err
	}
	var pages []page
	for _, n := range req.Pages {
		img := filepath.Join(dir, fmt.Sprintf("p%d", n))
		args := []string{"-r", strconv.Itoa(dpi), "-gray", "-png", "-singlefile", "-f", strconv.Itoa(n), "-l", strconv.Itoa(n)}
		if req.Password != "" {
			args = append(args, "-upw", req.Password)
		}
		if out, err := exec.Command("pdftoppm", append(args, input, img)...).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("pdftoppm page %d: %v: %s", n, err, out)
		}
		tsv, err := exec.Command("tesseract", img+".png", "-", "--dpi", strconv.Itoa(dpi), "-l", lang, "tsv").Output()
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) && bytes.Contains(exitErr.Stderr, []byte("traineddata")) {
				return nil, fmt.Errorf("%w: tesseract language data for %q is missing: %s", errUnavailable, lang, bytes.TrimSpace(exitErr.Stderr))
			}
			return nil, fmt.Errorf("tesseract page %d: %v", n, err)
		}
		pages = append(pages, parseTSV(n, tsv))
	}
	return pages, nil
}

// parseTSV groups tesseract's word rows into lines. Columns: level page_num
// block_num par_num line_num word_num left top width height conf text.
func parseTSV(n int, tsv []byte) page {
	result := page{Page: n, Lines: []line{}}
	type key struct{ block, par, line int }
	index := map[key]int{}
	var confSum []float64
	var confN []int
	for i, row := range strings.Split(string(tsv), "\n") {
		cols := strings.Split(row, "\t")
		if i == 0 || len(cols) < 12 {
			continue
		}
		num := func(c int) float64 { v, _ := strconv.ParseFloat(cols[c], 64); return v }
		switch cols[0] {
		case "1":
			result.Width, result.Height = num(8), num(9)
		case "5":
			text := strings.TrimSpace(cols[11])
			if text == "" {
				continue
			}
			k := key{int(num(2)), int(num(3)), int(num(4))}
			box := rect{num(6), num(7), num(6) + num(8), num(7) + num(9)}
			at, ok := index[k]
			if !ok {
				index[k] = len(result.Lines)
				result.Lines = append(result.Lines, line{Text: text, Box: box})
				confSum = append(confSum, 0)
				confN = append(confN, 0)
				at = len(result.Lines) - 1
			} else {
				ln := &result.Lines[at]
				ln.Text += " " + text
				ln.Box = rect{min(ln.Box.X0, box.X0), min(ln.Box.Y0, box.Y0), max(ln.Box.X1, box.X1), max(ln.Box.Y1, box.Y1)}
			}
			if conf := num(10); conf >= 0 {
				confSum[at] += conf / 100
				confN[at]++
			}
		}
	}
	for i := range result.Lines {
		if confN[i] > 0 {
			result.Lines[i].Confidence = confSum[i] / float64(confN[i])
		}
	}
	return result
}
