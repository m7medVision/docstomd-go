package docstomd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func convertFile(t *testing.T, path string) (*Result, error) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return Convert(context.Background(), bytes.NewReader(data), Options{FileName: path})
}

func TestConvertDocxConformance(t *testing.T) {
	fixtures, err := filepath.Glob(filepath.Join("testdata", "docx", "*.docx"))
	if err != nil {
		t.Fatal(err)
	}
	malformed, err := filepath.Glob(filepath.Join("testdata", "docx", "malformed", "*.docx"))
	if err != nil {
		t.Fatal(err)
	}
	if len(fixtures) < 11 || len(malformed) < 6 {
		t.Fatalf("docx corpus missing: %d fixtures, %d malformed", len(fixtures), len(malformed))
	}
	for _, path := range append(fixtures, malformed...) {
		name := strings.TrimSuffix(path, ".docx")
		t.Run(filepath.Base(name), func(t *testing.T) {
			result, err := convertFile(t, path)
			if strings.HasSuffix(name, "--errors") {
				if got := ErrorCodeOf(err); got != CodeMalformed {
					t.Fatalf("code = %q, want malformed (%v)", got, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(name + ".md")
			if err != nil {
				t.Fatal(err)
			}
			if result.Format != FormatDocx {
				t.Errorf("format = %v", result.Format)
			}
			if result.Markdown != string(want) {
				t.Errorf("markdown mismatch\n--- got\n%s\n--- want\n%s", result.Markdown, want)
			}
		})
	}
}

func TestConvertDocxAbuseIsResourceLimit(t *testing.T) {
	abuse, err := filepath.Glob(filepath.Join("testdata", "docx", "abuse", "*.docx"))
	if err != nil {
		t.Fatal(err)
	}
	if len(abuse) != 3 {
		t.Fatalf("abuse corpus has %d files", len(abuse))
	}
	for _, path := range abuse {
		_, err := convertFile(t, path)
		if got := ErrorCodeOf(err); got != CodeResourceLimit {
			t.Errorf("%s: code = %q, want resourceLimit (%v)", filepath.Base(path), got, err)
		}
		if !IsFatal(err) {
			t.Errorf("%s: resource limits are fatal", filepath.Base(path))
		}
	}
}
