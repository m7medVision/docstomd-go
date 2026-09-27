// Package catalog finds, lists and installs local OCR models.
package catalog

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
)

// EnvModels overrides where models are installed.
const EnvModels = "DOCSTOMD_OCR_MODELS"

// manifestFile marks an installed model directory.
const manifestFile = "manifest.json"

// Root is the directory holding installed models: $DOCSTOMD_OCR_MODELS, else
// <user data dir>/docstomd/ocr/models.
func Root() string {
	if dir := os.Getenv(EnvModels); dir != "" {
		return dir
	}
	return filepath.Join(dataDir(), "docstomd", "ocr", "models")
}

// dataDir is $XDG_DATA_HOME or ~/.local/share on Unix, the Application
// Support directory on macOS and %LocalAppData% on Windows.
func dataDir() string {
	switch runtime.GOOS {
	case "windows":
		if dir := os.Getenv("LocalAppData"); dir != "" {
			return dir
		}
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Application Support")
		}
	default:
		if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
			return dir
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "share")
	}
	return os.TempDir()
}

// Installed lists the ids of the models installed under root, sorted.
func Installed(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range entries {
		// Stat follows symlinked model directories.
		if _, err := os.Stat(filepath.Join(root, e.Name(), manifestFile)); err == nil {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	return ids
}
