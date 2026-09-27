package catalog

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/m7medVision/docstomd-go/ocr/engine"
)

// Format is the catalog format version this package reads.
const Format = 1

// Builtin is the name of the catalog shipped with the engine.
const Builtin = "builtin"

//go:embed builtin.json
var builtinJSON []byte

// Catalog lists installable models.
type Catalog struct {
	Format int     `json:"catalog"`
	Models []Entry `json:"models"`
	// Origin is Builtin, or the path or URL the catalog was read from.
	Origin string `json:"-"`
}

// Entry is one installable model.
type Entry struct {
	ID          string   `json:"id"`
	Family      string   `json:"family"`
	Description string   `json:"description,omitempty"`
	Languages   []string `json:"languages"`
	// License is the SPDX identifier of the weights' licence.
	License string `json:"license"`
	Files   []File `json:"files"`
	// Manifest is written to manifest.json at install.
	Manifest json.RawMessage `json:"manifest"`
	// Origin is the catalog the entry came from.
	Origin string `json:"-"`
}

// File is one downloaded file of a model.
type File struct {
	// Path is where the file goes inside the model directory.
	Path string `json:"path"`
	// Source is hf://<org>/<repo>@<commit>/<file> or an https:// URL.
	Source string `json:"source"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Size is the total download size.
func (e *Entry) Size() int64 {
	var n int64
	for _, f := range e.Files {
		n += f.Size
	}
	return n
}

// Permissive licences install without --accept-license.
var Permissive = []string{"Apache-2.0", "MIT", "BSD-2-Clause", "BSD-3-Clause"}

// IsPermissive reports whether the entry's licence needs no acceptance.
func (e *Entry) IsPermissive() bool {
	return slices.Contains(Permissive, e.License)
}

// Load returns the built-in catalog.
func Load() *Catalog {
	c, err := Parse(builtinJSON, Builtin)
	if err != nil {
		panic("builtin catalog: " + err.Error())
	}
	return c
}

var (
	idPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	hexSHA256   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	hfSource    = regexp.MustCompile(`^hf://([A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*)@([0-9a-f]{40})/(.+)$`)
	safeRelPath = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)
)

// Parse reads and validates a catalog.
func Parse(data []byte, origin string) (*Catalog, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var c Catalog
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("catalog %s: %v", origin, err)
	}
	if c.Format != Format {
		return nil, fmt.Errorf("catalog %s: format %d, want %d", origin, c.Format, Format)
	}
	c.Origin = origin
	seen := map[string]bool{}
	for i := range c.Models {
		e := &c.Models[i]
		e.Origin = origin
		if err := e.validate(); err != nil {
			return nil, fmt.Errorf("catalog %s: model %q: %v", origin, e.ID, err)
		}
		if seen[e.ID] {
			return nil, fmt.Errorf("catalog %s: model %q listed twice", origin, e.ID)
		}
		seen[e.ID] = true
	}
	return &c, nil
}

func (e *Entry) validate() error {
	if !idPattern.MatchString(e.ID) {
		return errors.New("id must be lowercase letters, digits, '.', '_' or '-'")
	}
	if e.License == "" {
		return errors.New("license is required")
	}
	if len(e.Files) == 0 {
		return errors.New("no files")
	}
	paths := map[string]bool{}
	for _, f := range e.Files {
		if !safeRelPath.MatchString(f.Path) || strings.Contains(f.Path, "..") || f.Path == engine.ManifestFile {
			return fmt.Errorf("file path %q is not a safe relative path", f.Path)
		}
		if paths[f.Path] {
			return fmt.Errorf("file %s listed twice", f.Path)
		}
		paths[f.Path] = true
		if !hexSHA256.MatchString(f.SHA256) {
			return fmt.Errorf("file %s: sha256 must be 64 lowercase hex digits", f.Path)
		}
		if f.Size <= 0 {
			return fmt.Errorf("file %s: size must be positive", f.Path)
		}
		if _, err := sourceURL(f.Source, ""); err != nil {
			return fmt.Errorf("file %s: %v", f.Path, err)
		}
	}
	var m engine.Manifest
	if err := json.Unmarshal(e.Manifest, &m); err != nil {
		return fmt.Errorf("manifest: %v", err)
	}
	if m.ID != e.ID {
		return fmt.Errorf("manifest id %q differs from the entry id", m.ID)
	}
	if err := m.Validate(); err != nil {
		return fmt.Errorf("manifest: %v", err)
	}
	for _, ref := range m.Files() {
		if !paths[ref] {
			return fmt.Errorf("manifest refers to %s, which the entry does not download", ref)
		}
	}
	return nil
}

// sourceURL resolves a file source to an https URL. hf:// sources must pin
// a full commit and resolve against endpoint (default huggingface.co).
func sourceURL(source, endpoint string) (string, error) {
	if m := hfSource.FindStringSubmatch(source); m != nil {
		if endpoint == "" {
			endpoint = "https://huggingface.co"
		}
		return strings.TrimRight(endpoint, "/") + "/" + m[1] + "/resolve/" + m[2] + "/" + m[3], nil
	}
	if strings.HasPrefix(source, "hf://") {
		return "", fmt.Errorf("source %q: want hf://<org>/<repo>@<40-hex commit>/<file>", source)
	}
	if strings.HasPrefix(source, "https://") && len(source) > len("https://") {
		return source, nil
	}
	return "", fmt.Errorf("source %q: only hf:// and https:// sources are allowed", source)
}

// hfEndpoint is $HF_ENDPOINT or empty for the default.
func hfEndpoint() string {
	return os.Getenv("HF_ENDPOINT")
}

// Set is the catalogs in effect, built-in first.
type Set []*Catalog

// Find returns the entry with id, searching catalogs in order.
func (s Set) Find(id string) (*Entry, bool) {
	for _, c := range s {
		for i := range c.Models {
			if c.Models[i].ID == id {
				return &c.Models[i], true
			}
		}
	}
	return nil, false
}

// Entries lists every entry in catalog order; an id listed again by a later
// catalog is shadowed by the first.
func (s Set) Entries() []*Entry {
	var out []*Entry
	seen := map[string]bool{}
	for _, c := range s {
		for i := range c.Models {
			if !seen[c.Models[i].ID] {
				seen[c.Models[i].ID] = true
				out = append(out, &c.Models[i])
			}
		}
	}
	return out
}
