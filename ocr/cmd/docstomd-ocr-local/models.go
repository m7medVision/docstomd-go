package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/m7medVision/docstomd-go/ocr/catalog"
)

// defaultModel is suggested when nothing is installed.
const defaultModel = "pp-ocrv5-mobile"

// resolveModel picks the model directory: --model-dir, else --model under
// the models root, else the only installed model, else the first installed
// one.
func resolveModel(opts options) (string, error) {
	if opts.modelDir != "" {
		if _, err := os.Stat(filepath.Join(opts.modelDir, "manifest.json")); err != nil {
			return "", fmt.Errorf("%w: no model manifest in %s", errUnavailable, opts.modelDir)
		}
		return opts.modelDir, nil
	}
	root := opts.modelsRoot
	if root == "" {
		root = catalog.Root()
	}
	installed := catalog.Installed(root)
	if opts.model != "" {
		if !slices.Contains(installed, opts.model) {
			return "", fmt.Errorf("%w: model %s is not installed in %s; run: docstomd ocr install %s", errUnavailable, opts.model, root, opts.model)
		}
		return filepath.Join(root, opts.model), nil
	}
	if len(installed) == 0 {
		return "", fmt.Errorf("%w: no OCR model is installed in %s; run: docstomd ocr install %s", errUnavailable, root, defaultModel)
	}
	return filepath.Join(root, installed[0]), nil
}
