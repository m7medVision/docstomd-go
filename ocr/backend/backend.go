// Package backend selects the GoMLX compute backend the local OCR engine
// runs on.
//
//   - go: pure Go, no downloads, the reference backend (slow).
//   - auto: the fastest backend whose runtime is installed, else go.
//
// Faster backends (onnx, xla) load native runtimes at run time, never
// through cgo.
package backend

import (
	"errors"
	"fmt"

	"github.com/gomlx/compute"
	_ "github.com/gomlx/compute/gobackend" // registers "go"
)

// Names lists the backends users can pick.
var Names = []string{"auto", "go"}

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
	case "", "auto", "go":
		b, err := compute.NewWithConfig("go")
		if err != nil {
			return nil, fmt.Errorf("%w: go: %v", ErrUnavailable, err)
		}
		return &Selection{Backend: b, Name: "go"}, nil
	}
	return nil, fmt.Errorf("%w: unknown backend %q (auto, go)", ErrUnavailable, name)
}
