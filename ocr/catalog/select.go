package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/m7medVision/docstomd-go/ocr/engine"
)

// ErrNotInstalled means no installed model fits the request.
var ErrNotInstalled = errors.New("model not installed")

// DefaultModel is suggested when nothing is installed.
const DefaultModel = "pp-ocrv5-mobile"

// maxCatalog bounds a catalog read from a URL.
const maxCatalog = 16 << 20

// LoadSet returns the built-in catalog followed by the user catalogs, each a
// file path or an https:// URL.
func LoadSet(ctx context.Context, user []string) (Set, error) {
	set := Set{Load()}
	for _, src := range user {
		data, err := readCatalog(ctx, src)
		if err != nil {
			return nil, fmt.Errorf("catalog %s: %v", src, err)
		}
		c, err := Parse(data, src)
		if err != nil {
			return nil, err
		}
		set = append(set, c)
	}
	return set, nil
}

func readCatalog(ctx context.Context, src string) ([]byte, error) {
	if !strings.HasPrefix(src, "https://") {
		return os.ReadFile(src)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET: %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxCatalog))
}

// Select picks an installed model directory under root: model when given;
// else, for lang, the first installed model listing it; else the only
// installed model; else the first installed one in catalog order.
func Select(set Set, root, model, lang string) (string, error) {
	installed := Installed(root)
	if model != "" {
		if !slices.Contains(installed, model) {
			return "", fmt.Errorf("%w: %s is not installed in %s; run: docstomd ocr install %s", ErrNotInstalled, model, root, model)
		}
		return filepath.Join(root, model), nil
	}
	ordered := inCatalogOrder(set, installed)
	if lang != "" {
		for _, id := range ordered {
			if hasLanguage(installedLanguages(filepath.Join(root, id)), lang) {
				return filepath.Join(root, id), nil
			}
		}
		hint := ""
		for _, e := range set.Entries() {
			if hasLanguage(e.Languages, lang) {
				hint = "; run: docstomd ocr install " + e.ID
				break
			}
		}
		return "", fmt.Errorf("%w: no installed model reads %q%s", ErrNotInstalled, lang, hint)
	}
	if len(ordered) == 0 {
		return "", fmt.Errorf("%w: no OCR model is installed in %s; run: docstomd ocr install %s", ErrNotInstalled, root, DefaultModel)
	}
	return filepath.Join(root, ordered[0]), nil
}

// installedLanguages reads the languages an installed model declares.
func installedLanguages(dir string) []string {
	data, err := os.ReadFile(filepath.Join(dir, engine.ManifestFile))
	if err != nil {
		return nil
	}
	var m struct {
		Languages []string `json:"languages"`
	}
	if json.Unmarshal(data, &m) != nil {
		return nil
	}
	return m.Languages
}

// hasLanguage matches a language code, also against a regional variant's
// base (zh matches zh-Hant).
func hasLanguage(langs []string, lang string) bool {
	for _, l := range langs {
		if strings.EqualFold(l, lang) || strings.EqualFold(strings.SplitN(l, "-", 2)[0], lang) {
			return true
		}
	}
	return false
}

// inCatalogOrder orders installed ids as the catalogs list them; ids no
// catalog lists come last, sorted.
func inCatalogOrder(set Set, installed []string) []string {
	var out []string
	for _, e := range set.Entries() {
		if slices.Contains(installed, e.ID) {
			out = append(out, e.ID)
		}
	}
	for _, id := range installed {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}
