package parse_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/m7medVision/docstomd-go/internal/pdf/internal/fuzzguard"
	"github.com/m7medVision/docstomd-go/internal/pdf/parse"
	"github.com/m7medVision/docstomd-go/internal/pdftest"
)

const (
	fuzzHeapBudget = 256 << 20
	fuzzTimeBudget = 5 * time.Second
	fuzzMaxInput   = 1 << 20
)

func addFixtureSeeds(f *testing.F) {
	f.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "..", "..", "testdata", "*", "*.pdf"))
	if err != nil {
		f.Fatalf("glob fixtures: %v", err)
	}
	if len(paths) == 0 {
		f.Fatal("no PDF fixtures found under testdata")
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			f.Fatalf("read fixture %s: %v", path, err)
		}
		if len(data) > fuzzMaxInput {
			continue
		}
		f.Add(data)
	}
	for _, data := range pdftest.Hostile() {
		f.Add(data)
	}
}

// FuzzParse loads arbitrary bytes as a PDF and decodes every content stream
// of every page. It asserts only that nothing panics, hangs, or exhausts memory.
func FuzzParse(f *testing.F) {
	addFixtureSeeds(f)
	guard := fuzzguard.Start(f, fuzzHeapBudget, fuzzTimeBudget)
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > fuzzMaxInput {
			t.Skip("input larger than fuzz cap")
		}
		guard.Begin()
		defer guard.End()
		doc, err := parse.Parse(data)
		if err != nil {
			return
		}
		for _, page := range doc.Pages() {
			doc.PageDict(page)
			doc.PageResources(page)
			for _, contentRef := range doc.PageContents(page) {
				obj, err := doc.GetObject(contentRef.Num)
				if err != nil {
					continue
				}
				doc.StreamData(obj)
			}
		}
	})
}
