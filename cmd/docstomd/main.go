package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/m7medVision/docstomd-go"
)

var version = "dev"

const (
	exitOK          = 0
	exitUsage       = 1
	exitUnsupported = 2
	exitNeedsOcr    = 3
	exitError       = 4
	// exitOCRUnavailable: OCR cannot run here or the engine broke the
	// protocol (ocrUnavailable, ocrProtocol).
	exitOCRUnavailable = 5
)

const usageText = `usage: docstomd <command> [flags] <file>

commands:
  convert   convert a document to Markdown
  detect    report the detected document format
  version   print the version

common flags:
  --json    emit machine-readable JSON

convert OCR flags (PDF):
  --ocr off|auto|force   off fails scanned PDFs with exit 3; auto OCRs routed pages; force OCRs all
  --ocr-dry-run          with --ocr auto|force, report billed pages and estimated cost without calling the provider
  --ocr-max-pages N      send at most N pages per document to OCR
  --ocr-max-pages-run N  send at most N pages to OCR across all inputs
  --ocr-provider P       mistral, local (docstomd-ocr-local), exec:<path> for any
                         engine speaking the docstomd OCR protocol
                         (docs/ocr-protocol.md), or a name from
                         $XDG_CONFIG_HOME/docstomd/providers.json;
                         default: mistral when MISTRAL_API_KEY is set, else local
  --ocr-model ID         Mistral model (default mistral-ocr-latest) or local model id
  Mistral reads MISTRAL_API_KEY from the environment.

convert several files:
  docstomd convert --out-dir DIR a.pdf b.pdf   one OCR engine session serves all inputs

run 'docstomd <command> --help' for every flag.

exit codes:
  0 success, 1 usage, 2 unsupported input, 3 OCR required, 4 conversion error,
  5 OCR engine unavailable or broke the protocol
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return exitUsage
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "convert":
		return runConvert(ctx, rest, stdout, stderr)
	case "detect":
		return runDetect(ctx, rest, stdout, stderr)
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usageText)
		return exitOK
	case "version", "--version":
		fmt.Fprintln(stdout, version)
		return exitOK
	default:
		fmt.Fprintf(stderr, "docstomd: unknown command %q\n\n%s", cmd, usageText)
		return exitUsage
	}
}

func runConvert(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("docstomd convert", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "usage: docstomd convert [flags] <file> [<file>...]\nflags:\n")
		fs.PrintDefaults()
	}
	jsonOut := fs.Bool("json", false, "emit JSON with metadata")
	itemsJSON := fs.Bool("items-json", false, "emit positioned extraction items as JSON")
	outDir := fs.String("out-dir", "", "write <name>.md (or .json with --json) per input into this directory; required for several inputs")
	ocrMode := fs.String("ocr", "off", "OCR mode: off, auto, or force")
	ocrDryRun := fs.Bool("ocr-dry-run", false, "with --ocr auto|force, report billed OCR pages and estimated cost without calling the provider")
	ocrMaxPages := fs.Int("ocr-max-pages", 0, "maximum pages sent to OCR per document (0 = unlimited)")
	ocrMaxPagesRun := fs.Int("ocr-max-pages-run", 0, "maximum pages sent to OCR across all inputs (0 = unlimited)")
	ocrModel := fs.String("ocr-model", "", "OCR model: the Mistral model id (default mistral-ocr-latest), or a local model id")
	ocrProvider := fs.String("ocr-provider", "", "OCR provider: mistral, local, exec:<path>, or a name from the config file (default: mistral when MISTRAL_API_KEY is set, else local)")
	if wantsHelp(args) {
		fs.SetOutput(stdout)
		fs.Usage()
		return exitOK
	}
	if err := fs.Parse(reorderFlags(args)); err != nil {
		return exitUsage
	}
	files := fs.Args()
	if len(files) == 0 || (len(files) > 1 && *outDir == "") {
		fmt.Fprintln(stderr, "docstomd convert: one input file is required (several need --out-dir)")
		fs.Usage()
		return exitUsage
	}
	if *itemsJSON {
		if len(files) != 1 || *outDir != "" {
			fmt.Fprintln(stderr, "docstomd convert: --items-json takes exactly one input and no --out-dir")
			return exitUsage
		}
		data, err := os.ReadFile(files[0])
		if err != nil {
			return reportError(stdout, stderr, *jsonOut, err)
		}
		items, err := docstomd.ExtractItems(ctx, bytes.NewReader(data))
		if err != nil {
			return reportError(stdout, stderr, *jsonOut, err)
		}
		return writeJSON(stdout, stderr, items)
	}
	var mode docstomd.OCRMode
	switch *ocrMode {
	case "off", "":
	case "auto":
		mode = docstomd.OCRAuto
	case "force":
		mode = docstomd.OCRForce
	default:
		fmt.Fprintf(stderr, "docstomd convert: unknown --ocr mode %q (off, auto, force)\n", *ocrMode)
		return exitUsage
	}
	if *ocrDryRun && mode == docstomd.OCROff {
		fmt.Fprintln(stderr, "docstomd convert: --ocr-dry-run needs --ocr auto or --ocr force")
		return exitUsage
	}
	outputs, err := outputPaths(files, *outDir, *jsonOut)
	if err != nil {
		fmt.Fprintf(stderr, "docstomd convert: %v\n", err)
		return exitUsage
	}
	var provider docstomd.OCRProvider
	if mode != docstomd.OCROff {
		cfg, err := loadConfig()
		if err != nil {
			fmt.Fprintf(stderr, "docstomd convert: %v\n", err)
			return exitUsage
		}
		provider, err = resolveProvider(providerFlags{spec: *ocrProvider, model: *ocrModel}, cfg)
		if err != nil {
			fmt.Fprintf(stderr, "docstomd convert: %v\n", err)
			return exitUsage
		}
		if closer, ok := provider.(io.Closer); ok {
			defer closer.Close()
		}
	}
	ocrOpts := docstomd.OCROptions{
		Mode:           mode,
		Provider:       provider,
		MaxPagesPerDoc: *ocrMaxPages,
		MaxPagesPerRun: *ocrMaxPagesRun,
		Run:            &docstomd.OCRRun{},
		DryRun:         *ocrDryRun,
	}
	if outputs == nil {
		return convertToStdout(ctx, files[0], ocrOpts, *jsonOut, stdout, stderr)
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return reportError(stdout, stderr, false, err)
	}
	exit := exitOK
	for i, file := range files {
		code := convertToFile(ctx, file, outputs[i], ocrOpts, *jsonOut, stderr)
		if exit == exitOK {
			exit = code
		}
		if ctx.Err() != nil {
			break
		}
	}
	return exit
}

func convertToStdout(ctx context.Context, file string, ocrOpts docstomd.OCROptions, jsonOut bool, stdout, stderr io.Writer) int {
	result, err := convertFile(ctx, file, ocrOpts)
	if err != nil {
		return reportError(stdout, stderr, jsonOut, err)
	}
	if jsonOut {
		return writeJSON(stdout, stderr, result)
	}
	fmt.Fprint(stdout, result.Markdown)
	return exitOK
}

// convertToFile converts one input of an --out-dir run; errors go to stderr
// prefixed with the input name.
func convertToFile(ctx context.Context, file, output string, ocrOpts docstomd.OCROptions, jsonOut bool, stderr io.Writer) int {
	result, err := convertFile(ctx, file, ocrOpts)
	if err != nil {
		fmt.Fprintf(stderr, "docstomd: %s: %s\n", file, humanError(err))
		return exitCodeFor(docstomd.ErrorCodeOf(err))
	}
	var body []byte
	if jsonOut {
		body, err = json.Marshal(result)
		body = append(body, '\n')
	} else {
		body = []byte(result.Markdown)
	}
	if err == nil {
		err = os.WriteFile(output, body, 0o644)
	}
	if err != nil {
		fmt.Fprintf(stderr, "docstomd: %s: %v\n", file, err)
		return exitError
	}
	return exitOK
}

func convertFile(ctx context.Context, file string, ocrOpts docstomd.OCROptions) (*docstomd.Result, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	return docstomd.Convert(ctx, bytes.NewReader(data), docstomd.Options{FileName: file, OCR: ocrOpts})
}

// outputPaths maps inputs to <outDir>/<name>.md (or .json); nil without
// outDir. Two inputs mapping to one output are rejected.
func outputPaths(files []string, outDir string, jsonOut bool) ([]string, error) {
	if outDir == "" {
		return nil, nil
	}
	ext := ".md"
	if jsonOut {
		ext = ".json"
	}
	seen := map[string]string{}
	out := make([]string, len(files))
	for i, file := range files {
		base := filepath.Base(file)
		path := filepath.Join(outDir, strings.TrimSuffix(base, filepath.Ext(base))+ext)
		if prev, ok := seen[path]; ok {
			return nil, fmt.Errorf("%s and %s would both write %s", prev, file, path)
		}
		seen[path] = file
		out[i] = path
	}
	return out, nil
}

func runDetect(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("docstomd detect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "usage: docstomd detect [flags] <file>\nflags:\n")
		fs.PrintDefaults()
	}
	jsonOut := fs.Bool("json", false, "emit JSON")
	if wantsHelp(args) {
		fs.SetOutput(stdout)
		fs.Usage()
		return exitOK
	}
	if err := fs.Parse(reorderFlags(args)); err != nil {
		return exitUsage
	}
	files := fs.Args()
	if len(files) != 1 {
		fmt.Fprintln(stderr, "docstomd detect: exactly one input file is required")
		fs.Usage()
		return exitUsage
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		return reportError(stdout, stderr, *jsonOut, err)
	}
	detection, err := docstomd.Detect(ctx, bytes.NewReader(data), files[0])
	if err != nil {
		return reportError(stdout, stderr, *jsonOut, err)
	}
	if *jsonOut {
		return writeJSON(stdout, stderr, detection)
	}
	fmt.Fprintln(stdout, detection.Format)
	return exitOK
}

func writeJSON(stdout, stderr io.Writer, v any) int {
	if err := json.NewEncoder(stdout).Encode(v); err != nil {
		fmt.Fprintf(stderr, "docstomd: encoding result: %v\n", err)
		return exitError
	}
	return exitOK
}

func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "--help" || a == "-h" {
			return true
		}
	}
	return false
}

var flagsTakingValue = map[string]bool{
	"--ocr": true, "-ocr": true,
	"--ocr-max-pages": true, "-ocr-max-pages": true,
	"--ocr-model": true, "-ocr-model": true,
	"--ocr-provider": true, "-ocr-provider": true,
	"--ocr-max-pages-run": true, "-ocr-max-pages-run": true,
	"--out-dir": true, "-out-dir": true,
}

func reorderFlags(args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			if flagsTakingValue[a] && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positional = append(positional, a)
	}
	return append(flags, positional...)
}

func reportError(stdout, stderr io.Writer, jsonOut bool, err error) int {
	code := docstomd.ErrorCodeOf(err)
	if jsonOut {
		if jsonErr := writeErrorJSON(stdout, err); jsonErr != nil {
			fmt.Fprintf(stderr, "docstomd: %s\n", humanError(err))
		}
		return exitCodeFor(code)
	}
	fmt.Fprintf(stderr, "docstomd: %s\n", humanError(err))
	return exitCodeFor(code)
}

func humanError(err error) string {
	var e *docstomd.Error
	if errors.As(err, &e) {
		return e.Error()
	}
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return string(docstomd.CodeIO) + ": " + err.Error()
	}
	return err.Error()
}

func writeErrorJSON(w io.Writer, err error) error {
	var e *docstomd.Error
	if !errors.As(err, &e) {
		e = &docstomd.Error{Code: docstomd.ErrorCodeOf(err), Message: err.Error()}
	}
	return json.NewEncoder(w).Encode(struct {
		Error *docstomd.Error `json:"error"`
	}{Error: e})
}

func exitCodeFor(code docstomd.ErrorCode) int {
	switch code {
	case docstomd.CodeNeedsOcr:
		return exitNeedsOcr
	case docstomd.CodeUnsupported:
		return exitUnsupported
	case docstomd.CodeOCRUnavailable, docstomd.CodeOCRProtocol:
		return exitOCRUnavailable
	default:
		return exitError
	}
}
