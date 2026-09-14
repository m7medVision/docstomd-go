package formats_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/m7medVision/docstomd-go/internal/formats/docx"
	"github.com/m7medVision/docstomd-go/internal/formats/pptx"
	"github.com/m7medVision/docstomd-go/internal/formats/xlsx"
	"github.com/m7medVision/docstomd-go/internal/model"
	"github.com/m7medVision/docstomd-go/internal/render/gfm"
)

var parsers = map[string]func([]byte) (*model.Document, error){
	".docx": docx.Parse,
	".xlsx": xlsx.Parse,
	".pptx": pptx.Parse,
}

// BenchmarkOfficeConvert converts every office document under the
// directories in DOCSTOMD_BENCH_DIRS (colon-separated; default: the formats
// bench corpus) to Markdown in-process, one sub-benchmark per document.
func BenchmarkOfficeConvert(b *testing.B) {
	dirs := os.Getenv("DOCSTOMD_BENCH_DIRS")
	if dirs == "" {
		dirs = "../../testdata:../../bench/results/formats-generated"
	}
	for _, dir := range filepath.SplitList(dirs) {
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			parse, ok := parsers[strings.ToLower(filepath.Ext(path))]
			if err != nil || d.IsDir() || !ok || strings.Contains(path, "abuse") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				b.Fatal(err)
			}
			b.Run(filepath.Base(path), func(b *testing.B) {
				b.SetBytes(int64(len(data)))
				b.ReportAllocs()
				for b.Loop() {
					doc, err := parse(data)
					if err != nil {
						b.Skip(err)
					}
					gfm.Render(doc)
				}
			})
			return nil
		})
	}
}
