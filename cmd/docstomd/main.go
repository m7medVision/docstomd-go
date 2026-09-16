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
	"strings"

	"github.com/m7medVision/docstomd-go"
)

const (
	exitOK          = 0
	exitUsage       = 1
	exitUnsupported = 2
	exitNeedsOcr    = 3
	exitError       = 4
)

const usageText = `usage: docstomd <command> [flags] <file>

commands:
  convert   convert a document to Markdown
  detect    report the detected document format

common flags:
  --json    emit machine-readable JSON

convert OCR flags (PDF):
  --ocr off|auto|force   off fails scanned PDFs with exit 3; auto OCRs routed pages; force OCRs all
  --ocr-dry-run          report billed pages and estimated cost without calling the provider
  --ocr-max-pages N      bill at most N pages per document
  --ocr-model ID         pin the Mistral OCR model (default mistral-ocr-latest)
  OCR reads MISTRAL_API_KEY from the environment.

run 'docstomd <command> --help' for every flag.

exit codes:
  0 success, 1 usage, 2 unsupported input, 3 OCR required, 4 conversion error
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
	default:
		fmt.Fprintf(stderr, "docstomd: unknown command %q\n\n%s", cmd, usageText)
		return exitUsage
	}
}

func runConvert(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("docstomd convert", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "usage: docstomd convert [flags] <file>\nflags:\n")
		fs.PrintDefaults()
	}
	jsonOut := fs.Bool("json", false, "emit JSON with metadata")
	itemsJSON := fs.Bool("items-json", false, "emit positioned extraction items as JSON")
	ocrMode := fs.String("ocr", "off", "OCR mode: off, auto, or force")
	ocrDryRun := fs.Bool("ocr-dry-run", false, "report billed OCR pages and estimated cost without calling the provider")
	ocrMaxPages := fs.Int("ocr-max-pages", 0, "maximum pages billed for OCR (0 = unlimited)")
	ocrModel := fs.String("ocr-model", "", "Mistral OCR model id (default mistral-ocr-latest; pin a version for reproducibility)")
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
		fmt.Fprintln(stderr, "docstomd convert: exactly one input file is required")
		fs.Usage()
		return exitUsage
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		return reportError(stdout, stderr, *jsonOut, err)
	}
	if *itemsJSON {
		items, err := docstomd.ExtractItems(ctx, bytes.NewReader(data))
		if err != nil {
			return reportError(stdout, stderr, *jsonOut, err)
		}
		return writeJSON(stdout, stderr, items)
	}
	opts := docstomd.Options{FileName: files[0], OCR: docstomd.OCROptions{
		Provider:       docstomd.NewMistralProvider(docstomd.MistralOptions{Model: *ocrModel}),
		MaxPagesPerDoc: *ocrMaxPages,
		DryRun:         *ocrDryRun,
	}}
	switch *ocrMode {
	case "off", "":
	case "auto":
		opts.OCR.Mode = docstomd.OCRAuto
	case "force":
		opts.OCR.Mode = docstomd.OCRForce
	default:
		fmt.Fprintf(stderr, "docstomd convert: unknown --ocr mode %q (off, auto, force)\n", *ocrMode)
		return exitUsage
	}
	result, err := docstomd.Convert(ctx, bytes.NewReader(data), opts)
	if err != nil {
		return reportError(stdout, stderr, *jsonOut, err)
	}
	if *jsonOut {
		return writeJSON(stdout, stderr, result)
	}
	fmt.Fprint(stdout, result.Markdown)
	return exitOK
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
	default:
		return exitError
	}
}
