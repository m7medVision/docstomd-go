// Package backend selects the GoMLX compute backend the local OCR engine
// runs on.
//
//   - go: pure Go, no downloads, the reference backend (slow).
//   - onnx: ONNX Runtime, loaded at run time through purego (no cgo);
//     install it with `docstomd ocr install --backend onnx`.
//   - auto: onnx when its runtime is installed, else go with a hint.
package backend

import (
	"errors"
	"fmt"

	"github.com/gomlx/compute"
	_ "github.com/gomlx/compute/gobackend" // registers "go"

	"github.com/m7medVision/docstomd-go/ocr/catalog"
	onnxbackend "github.com/m7medVision/docstomd-go/ocr/internal/computeonnx"
)

// Names lists the backends users can pick.
var Names = []string{"auto", "go", "onnx"}

// ErrUnavailable means the backend cannot run on this machine.
var ErrUnavailable = errors.New("backend unavailable")

// Selection is a created backend and why it was chosen.
type Selection struct {
	Backend compute.Backend
	// Name is the backend actually used.
	Name string
	// Hint, when set, is a one-line suggestion for the user (for example
	// that a faster backend can be installed).
	Hint string
}

// New creates the named backend ("" means auto).
func New(name string) (*Selection, error) {
	switch name {
	case "go":
		return goBackend()
	case "onnx":
		return onnx()
	case "", "auto":
		if sel, err := onnx(); err == nil {
			return sel, nil
		}
		sel, err := goBackend()
		if err != nil {
			return nil, err
		}
		if _, err := catalog.RuntimeFor("onnx"); err == nil {
			sel.Hint = "using the slow pure-Go backend; for native speed run: docstomd ocr install --backend onnx"
		}
		return sel, nil
	}
	return nil, fmt.Errorf("%w: unknown backend %q (auto, go, onnx)", ErrUnavailable, name)
}

func goBackend() (*Selection, error) {
	b, err := compute.NewWithConfig("go")
	if err != nil {
		return nil, fmt.Errorf("%w: go: %v", ErrUnavailable, err)
	}
	return &Selection{Backend: b, Name: "go"}, nil
}

func onnx() (*Selection, error) {
	rt, err := catalog.RuntimeFor("onnx")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if !rt.Installed() {
		return nil, fmt.Errorf("%w: ONNX Runtime %s is not installed; run: docstomd ocr install --backend onnx", ErrUnavailable, rt.Version)
	}
	onnxbackend.EnableAutoInstall(false)
	b, err := compute.NewWithConfig(onnxbackend.BackendName + ":cpu," + rt.LibraryPath())
	if err != nil {
		return nil, fmt.Errorf("%w: onnx: %v", ErrUnavailable, err)
	}
	return &Selection{Backend: b, Name: "onnx"}, nil
}
