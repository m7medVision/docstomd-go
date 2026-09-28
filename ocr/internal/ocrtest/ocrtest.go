// Package ocrtest holds helpers shared by the OCR module's tests: locating
// installed test models, fixtures and goldens.
package ocrtest

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// EnvModels names a models root (as installed by docstomd ocr install) for
// tests that need real models. Tests skip when it is unset or lacks the model.
const EnvModels = "DOCSTOMD_OCR_TEST_MODELS"

// EnvBackend picks the backend model tests run on (default go, the
// reference); fast backends must reproduce the same goldens.
const EnvBackend = "DOCSTOMD_OCR_TEST_BACKEND"

// Backend is the backend model tests run on.
func Backend() string {
	if b := os.Getenv(EnvBackend); b != "" {
		return b
	}
	return "go"
}

// ModelDir returns the directory of an installed model or skips the test.
func ModelDir(t testing.TB, id string) string {
	t.Helper()
	root := os.Getenv(EnvModels)
	if root == "" {
		t.Skipf("set %s to a models root with %s installed to run model tests", EnvModels, id)
	}
	dir := filepath.Join(root, id)
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
		t.Skipf("model %s is not installed in %s", id, root)
	}
	return dir
}

// RepoRoot is the repository root (the core module).
func RepoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..")
}

// Fixture reads testdata/ocr/<name> from the repository root.
func Fixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(RepoRoot(), "testdata", "ocr", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// GoldenLine is one expected line; Box is x0,y0,x1,y1 in image pixels.
type GoldenLine struct {
	Text string     `json:"text"`
	Box  [4]float64 `json:"box"`
}

// Golden reads ocr/testdata/goldens/<name>.json.
func Golden(t testing.TB, name string) []GoldenLine {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(RepoRoot(), "ocr", "testdata", "goldens", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Lines []GoldenLine `json:"lines"`
	}
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatal(err)
	}
	return g.Lines
}

// BoxTolerance is how far, in pixels, a recognized box edge may be from its
// golden.
const BoxTolerance = 8

// CompareLines checks recognized lines against a golden: same count, same
// text ignoring whitespace, boxes within BoxTolerance.
func CompareLines(t testing.TB, got []GoldenLine, want []GoldenLine) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("got %d lines, want %d:\n%s", len(got), len(want), dump(got))
		return
	}
	for i := range want {
		if squash(got[i].Text) != squash(want[i].Text) {
			t.Errorf("line %d = %q, want %q", i, got[i].Text, want[i].Text)
		}
		for k := range 4 {
			if math.Abs(got[i].Box[k]-want[i].Box[k]) > BoxTolerance {
				t.Errorf("line %d (%q) box %v, want %v ± %d", i, want[i].Text, got[i].Box, want[i].Box, BoxTolerance)
				break
			}
		}
	}
}

func squash(s string) string {
	return strings.Join(strings.Fields(s), "")
}

func dump(lines []GoldenLine) string {
	var sb strings.Builder
	for _, l := range lines {
		b, _ := json.Marshal(l)
		sb.Write(b)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// CompareWords checks word-level recognition against a fixture's text file
// (testdata/ocr/<name>.txt): the multiset of recognized words equals the
// text's, ignoring bare punctuation tokens such as list dashes, whose
// detection varies between word-level detectors.
func CompareWords(t testing.TB, got []GoldenLine, textFixture string) {
	t.Helper()
	count := func(words []string) map[string]int {
		m := map[string]int{}
		for _, w := range words {
			if strings.ContainsFunc(w, func(r rune) bool { return r != '-' && r != '•' }) {
				m[w]++
			}
		}
		return m
	}
	var have []string
	for _, l := range got {
		have = append(have, strings.Fields(l.Text)...)
	}
	want := strings.Fields(string(Fixture(t, textFixture)))
	h, w := count(have), count(want)
	for word, n := range w {
		if h[word] != n {
			t.Errorf("word %q recognized %d times, want %d", word, h[word], n)
		}
	}
	for word, n := range h {
		if w[word] == 0 {
			t.Errorf("unexpected word %q (%d times)", word, n)
		}
	}
}
