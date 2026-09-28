package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/m7medVision/docstomd-go/internal/ocr/external"
)

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

const ocrUsage = `usage: docstomd ocr <command> [flags]

commands (run by the local engine, docstomd-ocr-local):
  list [--json]                         catalog models and what is installed
  install [--accept-license L] <id>...  download and verify models
  install --backend onnx                install ONNX Runtime for native speed
  check-catalog                         verify catalog pins against Hugging Face

flags:
  --catalog C   extra model catalog file or https URL (repeatable); catalogs
                from the config file are always included
  --models DIR  models directory (default $DOCSTOMD_OCR_MODELS, else the
                user data directory)
`

// runOCR hands model management to the local engine binary.
func runOCR(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(stdout, ocrUsage)
		if len(args) == 0 {
			return exitUsage
		}
		return exitOK
	}
	switch args[0] {
	case "list", "install", "check-catalog":
	default:
		fmt.Fprintf(stderr, "docstomd ocr: unknown command %q\n\n%s", args[0], ocrUsage)
		return exitUsage
	}
	engine := external.FindLocal()
	if engine == "" {
		fmt.Fprintf(stderr, "docstomd ocr: %s not found; %s\n", external.LocalEngine, external.LocalHint)
		return exitOCRUnavailable
	}
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(stderr, "docstomd ocr: %v\n", err)
		return exitUsage
	}
	full := append([]string{args[0]}, catalogArgs(cfg.Catalogs)...)
	full = append(full, args[1:]...)
	cmd := exec.CommandContext(ctx, engine, full...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(stderr, "docstomd ocr: %v\n", err)
		return exitOCRUnavailable
	}
	return exitOK
}
