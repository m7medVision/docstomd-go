package external

import (
	"os"
	"os/exec"
	"path/filepath"
)

// LocalEngine is the name of docstomd's local OCR engine binary.
const LocalEngine = "docstomd-ocr-local"

// EnvLocalEngine names an explicit path to the local engine.
const EnvLocalEngine = "DOCSTOMD_OCR_LOCAL"

// LocalHint tells users how to get the local engine.
const LocalHint = "install it with 'go install github.com/m7medVision/docstomd-go/ocr/cmd/docstomd-ocr-local@latest', put it next to docstomd or on PATH, or set " + EnvLocalEngine

// FindLocal locates the local engine: $DOCSTOMD_OCR_LOCAL, then next to the
// running executable, then PATH. It returns "" when none is found.
func FindLocal() string {
	if path := os.Getenv(EnvLocalEngine); path != "" {
		return path
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), LocalEngine)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate
		}
	}
	if path, err := exec.LookPath(LocalEngine); err == nil {
		return path
	}
	return ""
}

// LocalConfig returns the config for the local engine with args, found by
// FindLocal. When it is not found, requests fail with ErrUnavailable and an
// install hint.
func LocalConfig(args ...string) Config {
	cmd := FindLocal()
	if cmd == "" {
		cmd = LocalEngine
	}
	return Config{Name: "local", Command: cmd, Args: args, Local: true, Hint: LocalHint}
}
