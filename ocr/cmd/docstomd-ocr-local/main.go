// Command docstomd-ocr-local is docstomd's local OCR engine: it runs OCR
// models on the CPU, without cgo, and speaks the docstomd OCR protocol
// (docs/ocr-protocol.md) on stdin/stdout.
//
// docstomd starts it for --ocr-provider local. Run by hand:
//
//	docstomd-ocr-local [--model ID | --model-dir DIR] [--backend auto|go]
package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/m7medVision/docstomd-go/ocr/backend"
	"github.com/m7medVision/docstomd-go/ocr/engine"
	"github.com/m7medVision/docstomd-go/ocr/pageimage"
)

var version = "dev"

// maxRequest bounds one request line (the PDF travels base64-encoded).
const maxRequest = 512 << 20

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "list":
			return runList(ctx, args[1:], stdout, stderr)
		case "install":
			return runInstall(ctx, args[1:], stderr)
		case "check-catalog":
			return runCheck(ctx, args[1:], stdout, stderr)
		case "serve":
			args = args[1:]
		}
	}
	fs := flag.NewFlagSet("docstomd-ocr-local", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), usage)
		fs.PrintDefaults()
	}
	opts := options{}
	fs.StringVar(&opts.model, "model", "", "model id to use (default: the only installed model, else the first installed in catalog order)")
	fs.StringVar(&opts.lang, "lang", "", "pick the first installed model that reads this language (e.g. en, ar)")
	fs.StringVar(&opts.modelDir, "model-dir", "", "use the model in this directory instead of an installed one")
	fs.StringVar(&opts.modelsRoot, "models", "", "directory holding installed models (default $DOCSTOMD_OCR_MODELS, else the user data directory)")
	fs.StringVar(&opts.backend, "backend", "auto", "inference backend: "+strings.Join(backend.Names, ", "))
	fs.Var(&opts.catalogs, "catalog", "extra model catalog (file or https URL); repeatable")
	fs.IntVar(&opts.renderDPI, "render-dpi", 200, "resolution for pages rendered with pdftoppm (pages without an extractable image)")
	showVersion := fs.Bool("version", false, "print the version")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *showVersion {
		fmt.Fprintln(stdout, version)
		return 0
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "docstomd-ocr-local: unexpected argument %q\n", fs.Arg(0))
		return 1
	}
	if opts.renderDPI < 36 || opts.renderDPI > 600 {
		fmt.Fprintln(stderr, "docstomd-ocr-local: --render-dpi must be between 36 and 600")
		return 1
	}
	s := &server{opts: opts, stderr: stderr}
	defer s.close()
	return s.serve(ctx, stdin, stdout)
}

const usage = `usage:
  docstomd-ocr-local [serve] [flags]      speak the docstomd OCR protocol on stdin/stdout
  docstomd-ocr-local list [--json]        list catalog models and what is installed
  docstomd-ocr-local install <id>...      download and verify models
  docstomd-ocr-local install --backend onnx   install the ONNX Runtime library (native speed)
  docstomd-ocr-local check-catalog        verify catalog pins against Hugging Face

serve flags:
`

type options struct {
	model      string
	lang       string
	modelDir   string
	modelsRoot string
	backend    string
	catalogs   stringList
	renderDPI  int
}

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

// Protocol v1 messages (docs/ocr-protocol.md).
type (
	request struct {
		Type     string `json:"type"`
		ID       string `json:"id"`
		PDF      string `json:"pdf"`
		Pages    []int  `json:"pages"`
		Password string `json:"password,omitempty"`
	}
	box struct {
		X0 float64 `json:"x0"`
		Y0 float64 `json:"y0"`
		X1 float64 `json:"x1"`
		Y1 float64 `json:"y1"`
	}
	line struct {
		Text       string  `json:"text"`
		Box        box     `json:"box"`
		Confidence float64 `json:"confidence"`
	}
	pageResult struct {
		Page   int     `json:"page"`
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
		Lines  []line  `json:"lines"`
	}
)

type server struct {
	opts   options
	stderr io.Writer
	eng    *engine.Engine
	// loadErr is kept so a missing model is reported on every request
	// without retrying the load.
	loadErr error
}

func (s *server) serve(ctx context.Context, stdin io.Reader, stdout io.Writer) int {
	out := json.NewEncoder(stdout)
	if err := out.Encode(map[string]any{"type": "hello", "protocol": 1, "engine": "docstomd-ocr-local", "version": version, "local": true}); err != nil {
		return 1
	}
	in := bufio.NewReaderSize(stdin, 1<<20)
	for {
		msg, err := readLine(in)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				fmt.Fprintln(s.stderr, "docstomd-ocr-local:", err)
				return 2
			}
			return 0
		}
		if len(msg) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(msg, &req); err != nil || req.Type != "recognize" {
			fmt.Fprintf(s.stderr, "docstomd-ocr-local: bad request: %.200s\n", msg)
			return 2
		}
		pages, err := s.recognize(ctx, req)
		var reply map[string]any
		switch {
		case errors.Is(err, errUnavailable):
			reply = map[string]any{"type": "error", "id": req.ID, "code": "unavailable", "message": err.Error()}
		case err != nil:
			reply = map[string]any{"type": "error", "id": req.ID, "code": "failed", "message": err.Error()}
		default:
			reply = map[string]any{"type": "result", "id": req.ID, "pages": pages}
		}
		if err := out.Encode(reply); err != nil {
			return 1
		}
		if ctx.Err() != nil {
			return 1
		}
	}
}

func readLine(r *bufio.Reader) ([]byte, error) {
	var msg []byte
	for {
		chunk, err := r.ReadSlice('\n')
		msg = append(msg, chunk...)
		if len(msg) > maxRequest {
			return nil, fmt.Errorf("request larger than %d bytes", maxRequest)
		}
		switch {
		case err == nil:
			return trimSpace(msg), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(trimSpace(msg)) > 0:
			return trimSpace(msg), nil
		default:
			return nil, err
		}
	}
}

func trimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

var errUnavailable = errors.New("unavailable")

func (s *server) recognize(ctx context.Context, req request) ([]pageResult, error) {
	eng, err := s.engine()
	if err != nil {
		return nil, err
	}
	pdf, err := base64.StdEncoding.DecodeString(req.PDF)
	if err != nil {
		return nil, fmt.Errorf("pdf is not base64: %v", err)
	}
	doc, err := pageimage.Open(pdf)
	if err != nil {
		return nil, fmt.Errorf("reading pdf: %v", err)
	}
	pages := []pageResult{}
	for _, n := range req.Pages {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		page, err := doc.Extract(n)
		if errors.Is(err, pageimage.ErrNoImage) {
			// CCITT, JBIG2, JPEG 2000 or vector-drawn text: render instead.
			page, err = doc.Render(ctx, pdf, n, s.opts.renderDPI, req.Password)
			if errors.Is(err, pageimage.ErrNoRenderer) {
				err = fmt.Errorf("no extractable image and pdftoppm (poppler) is not installed to render it")
			}
		}
		if err != nil {
			// Unanswered pages keep their native text and are flagged for
			// review by docstomd.
			fmt.Fprintf(s.stderr, "docstomd-ocr-local: page %d: %v\n", n, err)
			continue
		}
		lines, err := eng.Recognize(page.Image)
		if err != nil {
			return nil, fmt.Errorf("page %d: %v", n, err)
		}
		result := pageResult{Page: n, Width: page.Width, Height: page.Height, Lines: []line{}}
		for _, ln := range lines {
			x0, y0 := page.ToPage(ln.X0, ln.Y0)
			x1, y1 := page.ToPage(ln.X1, ln.Y1)
			result.Lines = append(result.Lines, line{Text: ln.Text, Box: box{x0, y0, x1, y1}, Confidence: ln.Confidence})
		}
		pages = append(pages, result)
	}
	return pages, nil
}

// engine loads the model on first use.
func (s *server) engine() (*engine.Engine, error) {
	if s.eng != nil || s.loadErr != nil {
		return s.eng, s.loadErr
	}
	dir, err := resolveModel(context.Background(), s.opts, s.stderr)
	if err != nil {
		s.loadErr = err
		return nil, err
	}
	sel, err := backend.New(s.opts.backend)
	if err != nil {
		s.loadErr = fmt.Errorf("%w: %v", errUnavailable, err)
		return nil, s.loadErr
	}
	if sel.Hint != "" {
		fmt.Fprintln(s.stderr, "docstomd-ocr-local:", sel.Hint)
	}
	eng, err := engine.Load(dir, sel.Backend)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			err = fmt.Errorf("%w: model in %s is incomplete (%v); reinstall it with: docstomd ocr install", errUnavailable, dir, err)
		}
		s.loadErr = err
		return nil, err
	}
	s.eng = eng
	return eng, nil
}

func (s *server) close() {
	if s.eng != nil {
		s.eng.Close()
	}
}
