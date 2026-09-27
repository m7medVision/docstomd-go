package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/m7medVision/docstomd-go"
	"github.com/m7medVision/docstomd-go/internal/ocr/external/externaltest"
)

// The test binary doubles as a stub OCR engine for exec: providers.
func TestMain(m *testing.M) {
	if mode := os.Getenv(externaltest.EnvMode); mode != "" {
		os.Exit(externaltest.Stub(mode))
	}
	// Keep the user's provider config and local engine out of the tests.
	dir, err := os.MkdirTemp("", "docstomd-cli-test")
	if err != nil {
		panic(err)
	}
	for key, value := range map[string]string{"XDG_CONFIG_HOME": dir, "HOME": dir, "PATH": ""} {
		if err := os.Setenv(key, value); err != nil {
			panic(err)
		}
	}
	if err := os.Unsetenv("DOCSTOMD_OCR_LOCAL"); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// writeConfig writes the provider config for this test.
func writeConfig(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "docstomd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docstomd", "providers.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// stubEngine returns an exec: provider spec for the stub engine in mode.
func stubEngine(t *testing.T, mode string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(externaltest.EnvMode, mode)
	return "exec:" + exe
}

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input.bin")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNoArgsPrintsUsageAndExitsOne(t *testing.T) {
	code, _, stderr := runCLI(t)
	if code != exitUsage {
		t.Errorf("exit = %d, want %d", code, exitUsage)
	}
	for _, want := range []string{"convert", "detect"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q:\n%s", want, stderr)
		}
	}
}

func TestHelpExitsZero(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}} {
		code, stdout, _ := runCLI(t, args...)
		if code != exitOK {
			t.Errorf("%v: exit = %d, want %d", args, code, exitOK)
		}
		for _, want := range []string{"convert", "--ocr-dry-run", "--ocr-max-pages", "MISTRAL_API_KEY"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("%v: usage missing %q:\n%s", args, want, stdout)
			}
		}
	}
}

func TestSubcommandHelpExitsZero(t *testing.T) {
	for _, cmd := range []string{"convert", "detect"} {
		code, stdout, _ := runCLI(t, cmd, "--help")
		if code != exitOK {
			t.Errorf("%s --help: exit = %d, want %d", cmd, code, exitOK)
		}
		if !strings.Contains(stdout, "usage") {
			t.Errorf("%s --help: stdout missing usage:\n%s", cmd, stdout)
		}
	}
}

func TestUnknownCommandExitsOne(t *testing.T) {
	code, _, stderr := runCLI(t, "frobnicate")
	if code != exitUsage {
		t.Errorf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "unknown command") {
		t.Errorf("stderr missing unknown-command note:\n%s", stderr)
	}
}

func TestConvertWithoutFileExitsOne(t *testing.T) {
	if code, _, _ := runCLI(t, "convert"); code != exitUsage {
		t.Errorf("exit = %d, want %d", code, exitUsage)
	}
	if code, _, _ := runCLI(t, "convert", "a", "b"); code != exitUsage {
		t.Errorf("two files: exit = %d, want %d", code, exitUsage)
	}
}

func TestConvertMissingFileIsIOError(t *testing.T) {
	code, _, stderr := runCLI(t, "convert", filepath.Join(t.TempDir(), "nope.bin"))
	if code != exitError {
		t.Errorf("exit = %d, want %d", code, exitError)
	}
	if !strings.Contains(stderr, "io") {
		t.Errorf("stderr missing io code:\n%s", stderr)
	}
}

func TestConvertMissingFileJSONShape(t *testing.T) {
	code, stdout, _ := runCLI(t, "convert", "--json", filepath.Join(t.TempDir(), "nope.bin"))
	if code != exitError {
		t.Errorf("exit = %d, want %d", code, exitError)
	}
	var got struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout not JSON: %v\n%s", err, stdout)
	}
	if got.Error.Code != "io" {
		t.Errorf("code = %q, want io", got.Error.Code)
	}
}

func TestConvertUnsupportedJSON(t *testing.T) {
	file := writeFile(t, "not a supported document")
	code, stdout, _ := runCLI(t, "convert", "--json", file)
	if code != exitUnsupported {
		t.Errorf("exit = %d, want %d", code, exitUnsupported)
	}
	var got struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout not JSON: %v\n%s", err, stdout)
	}
	if got.Error.Code != "unsupported" {
		t.Errorf("code = %q, want unsupported", got.Error.Code)
	}
	if got.Error.Message == "" {
		t.Error("error message is empty")
	}
}

func TestConvertUnsupportedTextMode(t *testing.T) {
	file := writeFile(t, "not a supported document")
	code, _, stderr := runCLI(t, "convert", file)
	if code != exitUnsupported {
		t.Errorf("exit = %d, want %d", code, exitUnsupported)
	}
	if !strings.Contains(stderr, "unsupported") {
		t.Errorf("stderr missing unsupported:\n%s", stderr)
	}
}

func TestDetectUnknownContentJSON(t *testing.T) {
	file := writeFile(t, "not a supported document")
	code, stdout, _ := runCLI(t, "detect", "--json", file)
	if code != exitOK {
		t.Errorf("exit = %d, want %d", code, exitOK)
	}
	if !strings.Contains(stdout, `"format":"unknown"`) {
		t.Errorf("stdout missing unknown format:\n%s", stdout)
	}
}

func TestDetectPDFSuccess(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "detect", "cropbox_offset_origin.pdf")
	code, stdout, _ := runCLI(t, "detect", "--json", path)
	if code != exitOK {
		t.Errorf("json exit = %d, want %d", code, exitOK)
	}
	for _, want := range []string{`"format":"pdf"`, `"pdf_type":"text_based"`} {
		if !strings.Contains(stdout, want) {
			t.Errorf("json stdout missing %s:\n%s", want, stdout)
		}
	}
	code, stdout, _ = runCLI(t, "detect", path)
	if code != exitOK {
		t.Errorf("text exit = %d, want %d", code, exitOK)
	}
	if strings.TrimSpace(stdout) != "pdf" {
		t.Errorf("text stdout = %q, want pdf", stdout)
	}
}

func TestDetectFakePDFIsMalformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fake.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.7\nfake"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ := runCLI(t, "detect", "--json", path)
	if code != exitError {
		t.Errorf("exit = %d, want %d", code, exitError)
	}
	if !strings.Contains(stdout, `"code":"malformed"`) {
		t.Errorf("json stdout missing malformed code:\n%s", stdout)
	}
}

func TestDetectEncryptedPDFExitCode(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "detect", "encrypted-secret123.pdf")
	code, stdout, _ := runCLI(t, "detect", "--json", path)
	if code != exitError {
		t.Errorf("exit = %d, want %d", code, exitError)
	}
	if !strings.Contains(stdout, `"code":"encrypted"`) {
		t.Errorf("json stdout missing encrypted code:\n%s", stdout)
	}
}

func TestDetectContentWinsOverFileName(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "detect", "cropbox_offset_origin.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "mislabeled.docx")
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ := runCLI(t, "detect", "--json", path)
	if code != exitOK {
		t.Errorf("exit = %d, want %d", code, exitOK)
	}
	if !strings.Contains(stdout, `"format":"pdf"`) {
		t.Errorf("stdout missing pdf despite name:\n%s", stdout)
	}
}

func TestExitCodeMapping(t *testing.T) {
	cases := map[docstomd.ErrorCode]int{
		docstomd.CodeNeedsOcr:       exitNeedsOcr,
		docstomd.CodeUnsupported:    exitUnsupported,
		docstomd.CodeMalformed:      exitError,
		docstomd.CodeEncrypted:      exitError,
		docstomd.CodeIO:             exitError,
		docstomd.CodeOCRUnavailable: exitOCRUnavailable,
		docstomd.CodeOCRProtocol:    exitOCRUnavailable,
	}
	for code, want := range cases {
		if got := exitCodeFor(code); got != want {
			t.Errorf("exitCodeFor(%q) = %d, want %d", code, got, want)
		}
	}
}

func TestWrappedErrorJSONKeepsCode(t *testing.T) {
	wrapped := fmt.Errorf("reading page tree: %w", docstomd.Encrypted())
	var buf bytes.Buffer
	if err := writeErrorJSON(&buf, wrapped); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("payload not JSON: %v\n%s", err, buf.String())
	}
	if got.Error.Code != "encrypted" {
		t.Errorf("code = %q, want encrypted; payload: %s", got.Error.Code, buf.String())
	}
	if got.Error.Message == "" {
		t.Error("message dropped from wrapped error payload")
	}
}

func TestHumanErrorLabels(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"typed", docstomd.Encrypted(), "encrypted: document is encrypted"},
		{"wrapped typed", fmt.Errorf("ctx: %w", docstomd.Encrypted()), "encrypted: document is encrypted"},
		{"path error", &fs.PathError{Op: "open", Path: "/x", Err: os.ErrNotExist}, "io: open /x: "},
		{"context cancel", context.Canceled, "context canceled"},
	}
	for _, tc := range cases {
		if got := humanError(tc.err); !strings.HasPrefix(got, tc.want) {
			t.Errorf("%s: humanError = %q, want prefix %q", tc.name, got, tc.want)
		}
	}
}

func TestNeedsOcrErrorJSONPayload(t *testing.T) {
	var buf bytes.Buffer
	if err := writeErrorJSON(&buf, docstomd.NeedsOcr([]int{2, 3}, 10)); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Error struct {
			Code      string `json:"code"`
			Pages     []int  `json:"pages"`
			PageCount int    `json:"page_count"`
		} `json:"error"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("payload not JSON: %v\n%s", err, buf.String())
	}
	if got.Error.Code != "needsOcr" {
		t.Errorf("code = %q, want needsOcr", got.Error.Code)
	}
	if len(got.Error.Pages) != 2 || got.Error.Pages[0] != 2 || got.Error.Pages[1] != 3 {
		t.Errorf("pages = %v, want [2 3]", got.Error.Pages)
	}
	if got.Error.PageCount != 10 {
		t.Errorf("page_count = %d, want 10", got.Error.PageCount)
	}
}

func TestConvertPDFSuccessJSONSchema(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "detect", "cropbox_offset_origin.pdf")
	code, stdout, _ := runCLI(t, "convert", "--json", path)
	if code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	var got struct {
		Markdown   string `json:"markdown"`
		Format     string `json:"format"`
		PageCount  int    `json:"page_count"`
		DurationMS int64  `json:"processing_time_ms"`
		Layout     struct {
			IsComplex bool `json:"is_complex"`
		} `json:"layout"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout not JSON: %v\n%s", err, stdout)
	}
	if got.Format != "pdf" || got.PageCount != 1 || got.DurationMS < 0 {
		t.Errorf("schema fields: format=%q page_count=%d duration=%d", got.Format, got.PageCount, got.DurationMS)
	}
	if !strings.Contains(got.Markdown, "Visible glyph") {
		t.Errorf("markdown missing body text: %q", got.Markdown)
	}
	if !strings.Contains(stdout, `"pages_needing_ocr":[]`) {
		t.Errorf("unrouted document must report an empty page list, not null:\n%.300s", stdout)
	}
}

func TestConvertXlsxPrintsMarkdown(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "detect", "handmade-merged.xlsx")
	code, stdout, stderr := runCLI(t, "convert", path)
	if code != exitOK {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	if !strings.HasPrefix(stdout, "|  |  |  |\n| --- | --- | --- |\n| Merged across |") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestConvertRelocatedPptx(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "pptx", "handmade-altpath.pptx")
	code, stdout, stderr := runCLI(t, "convert", path)
	if code != exitOK {
		t.Fatalf("exit = %d, want 0\n%s", code, stderr)
	}
	if stdout != "## Relocated deck title\n" {
		t.Errorf("stdout = %q", stdout)
	}
	code, stdout, _ = runCLI(t, "detect", "--json", path)
	if code != exitOK || !strings.Contains(stdout, `"format":"pptx"`) {
		t.Errorf("detect exit = %d, stdout = %s", code, stdout)
	}
}

func TestConvertNeedsOcrExitCode(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "detect", "handmade-scanned.pdf")
	code, stdout, _ := runCLI(t, "convert", "--json", path)
	if code != exitNeedsOcr {
		t.Errorf("exit = %d, want %d", code, exitNeedsOcr)
	}
	if !strings.Contains(stdout, `"code":"needsOcr"`) || !strings.Contains(stdout, `"pages":[1,2]`) {
		t.Errorf("json missing needsOcr payload:\n%s", stdout)
	}
}

func TestConvertOCRFlagModes(t *testing.T) {
	t.Setenv("MISTRAL_API_KEY", "")
	scanned := filepath.Join("..", "..", "testdata", "detect", "handmade-scanned.pdf")
	code, stdout, _ := runCLI(t, "convert", "--ocr", "auto", "--ocr-provider", "mistral", "--json", scanned)
	if code != exitError {
		t.Errorf("auto without key exit = %d, want %d", code, exitError)
	}
	if !strings.Contains(stdout, "MISTRAL_API_KEY is not set") {
		t.Errorf("json stdout missing actionable key error:\n%s", stdout)
	}
	if !strings.Contains(stdout, `"code":"ocrAuth"`) {
		t.Errorf("json stdout missing ocrAuth code:\n%s", stdout)
	}
	code, _, _ = runCLI(t, "convert", "--ocr", "bogus", scanned)
	if code != exitUsage {
		t.Errorf("bogus mode exit = %d, want %d", code, exitUsage)
	}
	code, _, _ = runCLI(t, "convert", "--ocr", "off", scanned, "--json")
	if code != exitNeedsOcr {
		t.Errorf("off mode exit = %d, want %d", code, exitNeedsOcr)
	}
}

func TestConvertOCRDryRunReportsCostWithoutKey(t *testing.T) {
	t.Setenv("MISTRAL_API_KEY", "")
	scanned := filepath.Join("..", "..", "testdata", "detect", "handmade-scanned.pdf")
	code, stdout, stderr := runCLI(t, "convert", scanned, "--ocr", "auto", "--ocr-provider", "mistral", "--ocr-dry-run", "--ocr-max-pages", "1", "--json")
	if code != exitOK {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr)
	}
	var got struct {
		OCRCost struct {
			Provider    string  `json:"provider"`
			BilledPages []int   `json:"billed_pages"`
			Estimated   float64 `json:"estimated_cost_usd"`
			DryRun      bool    `json:"dry_run"`
			Truncated   bool    `json:"truncated_by_caps"`
		} `json:"ocr_cost"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout not JSON: %v\n%s", err, stdout)
	}
	c := got.OCRCost
	if c.Provider != "mistral" || fmt.Sprint(c.BilledPages) != "[1]" || !c.DryRun || !c.Truncated || c.Estimated != 0.004 {
		t.Errorf("ocr_cost = %+v, want capped mistral dry-run of page 1", c)
	}
}

func TestLiveMistralConvert(t *testing.T) {
	if os.Getenv("MISTRAL_API_KEY") == "" || os.Getenv("DOCSTOMD_LIVE_OCR") != "1" {
		t.Skip("live OCR test: set MISTRAL_API_KEY and DOCSTOMD_LIVE_OCR=1 (bills 1 page)")
	}
	path := filepath.Join("..", "..", "testdata", "detect", "scan_with_native_header_text.pdf")
	code, stdout, stderr := runCLI(t, "convert", "--ocr", "auto", "--ocr-max-pages", "1", "--json", path)
	if code != exitOK {
		t.Fatalf("exit = %d; stderr: %s; stdout: %s", code, stderr, stdout)
	}
	var got struct {
		Markdown string `json:"markdown"`
		OCRCost  struct {
			Provider    string  `json:"provider"`
			PagesBilled int     `json:"pages_billed"`
			Estimated   float64 `json:"estimated_cost_usd"`
		} `json:"ocr_cost"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout not JSON: %v\n%s", err, stdout)
	}
	if got.OCRCost.Provider != "mistral" || got.OCRCost.PagesBilled != 1 || got.OCRCost.Estimated <= 0 {
		t.Errorf("ocr_cost = %+v, want 1 billed mistral page with an estimate", got.OCRCost)
	}
	if !strings.Contains(strings.ToLower(got.Markdown), "order detail report") {
		t.Errorf("markdown = %.200q, want OCR text", got.Markdown)
	}
	if strings.Contains(stdout+stderr, os.Getenv("MISTRAL_API_KEY")) {
		t.Error("API key echoed in CLI output")
	}
}

func TestConvertBrokenOfficePackageIsMalformed(t *testing.T) {
	for _, name := range []string{"empty.docx", "empty.xlsx", "empty.pptx"} {
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		code, stdout, _ := runCLI(t, "convert", "--json", path)
		if code != exitError || !strings.Contains(stdout, `"code":"malformed"`) {
			t.Errorf("%s: exit = %d, stdout %s", name, code, stdout)
		}
	}
}

func TestConvertOCRDryRunNeedsOCRMode(t *testing.T) {
	scanned := filepath.Join("..", "..", "testdata", "detect", "handmade-scanned.pdf")
	cases := []struct {
		name string
		args []string
	}{
		{"no mode", []string{"convert", "--ocr-dry-run", scanned}},
		{"mode off", []string{"convert", "--ocr", "off", "--ocr-dry-run", "--json", scanned}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runCLI(t, tc.args...)
			if code != exitUsage {
				t.Errorf("exit = %d, want %d", code, exitUsage)
			}
			if stdout != "" || !strings.Contains(stderr, "--ocr-dry-run needs --ocr auto or --ocr force") {
				t.Errorf("stdout = %q, stderr = %q, want usage message only", stdout, stderr)
			}
		})
	}
}

func TestConvertExecProvider(t *testing.T) {
	scanned := filepath.Join("..", "..", "testdata", "detect", "handmade-scanned.pdf")
	for _, mode := range []string{"markdown", "lines"} {
		t.Run(mode, func(t *testing.T) {
			code, stdout, stderr := runCLI(t, "convert", "--ocr", "auto", "--ocr-provider", stubEngine(t, mode), "--json", scanned)
			if code != exitOK {
				t.Fatalf("exit = %d, stderr:\n%s", code, stderr)
			}
			var result struct {
				Markdown string                  `json:"markdown"`
				OCRCost  *docstomd.OCRCostReport `json:"ocr_cost"`
			}
			if err := json.Unmarshal([]byte(stdout), &result); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(result.Markdown, "page 1 of %PDF") || !strings.Contains(result.Markdown, "page 2 of %PDF") {
				t.Errorf("markdown = %q", result.Markdown)
			}
			if result.OCRCost == nil || result.OCRCost.Provider != "stub" || result.OCRCost.PagesBilled != 2 {
				t.Errorf("cost = %+v", result.OCRCost)
			}
		})
	}
}

func TestConvertExecProviderFailuresExitFive(t *testing.T) {
	scanned := filepath.Join("..", "..", "testdata", "detect", "handmade-scanned.pdf")
	cases := []struct{ spec, code string }{
		{"exec:/nonexistent/engine", "ocrUnavailable"},
		{stubEngine(t, "malformed"), "ocrProtocol"},
	}
	for _, tc := range cases {
		code, stdout, _ := runCLI(t, "convert", "--ocr", "auto", "--ocr-provider", tc.spec, "--json", scanned)
		if code != exitOCRUnavailable {
			t.Errorf("%s: exit = %d, want %d", tc.spec, code, exitOCRUnavailable)
		}
		if !strings.Contains(stdout, `"code":"`+tc.code+`"`) {
			t.Errorf("%s: json missing %s:\n%s", tc.spec, tc.code, stdout)
		}
	}
	t.Setenv(externaltest.EnvMode, "unavailable")
	code, _, stderr := runCLI(t, "convert", "--ocr", "auto", "--ocr-provider", stubEngine(t, "unavailable"), scanned)
	if code != exitOCRUnavailable || !strings.Contains(stderr, "ocrUnavailable") || !strings.Contains(stderr, "stub") {
		t.Errorf("engine-reported unavailable: exit %d, stderr %q", code, stderr)
	}
}

func TestConvertUnknownProviderIsUsage(t *testing.T) {
	scanned := filepath.Join("..", "..", "testdata", "detect", "handmade-scanned.pdf")
	for _, spec := range []string{"bogus", "exec:"} {
		if code, _, _ := runCLI(t, "convert", "--ocr", "auto", "--ocr-provider", spec, scanned); code != exitUsage {
			t.Errorf("%q: exit = %d, want %d", spec, code, exitUsage)
		}
	}
}

func TestDefaultProviderRule(t *testing.T) {
	scanned := filepath.Join("..", "..", "testdata", "detect", "handmade-scanned.pdf")
	t.Setenv("MISTRAL_API_KEY", "")
	code, stdout, _ := runCLI(t, "convert", "--ocr", "auto", "--json", scanned)
	if code != exitOCRUnavailable || !strings.Contains(stdout, "docstomd-ocr-local") || !strings.Contains(stdout, "go install") {
		t.Errorf("no key: want local engine with install hint, exit %d:\n%s", code, stdout)
	}
	t.Setenv("MISTRAL_API_KEY", "k")
	code, stdout, _ = runCLI(t, "convert", "--ocr", "auto", "--ocr-dry-run", "--json", scanned)
	if code != exitOK || !strings.Contains(stdout, `"provider":"mistral"`) {
		t.Errorf("key set: want mistral, exit %d:\n%s", code, stdout)
	}
	t.Setenv("MISTRAL_API_KEY", "")
	t.Setenv("DOCSTOMD_OCR_LOCAL", stubEngine(t, "lines")[len("exec:"):])
	code, stdout, _ = runCLI(t, "convert", "--ocr", "auto", "--json", scanned)
	if code != exitOK || !strings.Contains(stdout, `"provider":"local"`) || !strings.Contains(stdout, `"local":true`) || !strings.Contains(stdout, "line on page 1") {
		t.Errorf("DOCSTOMD_OCR_LOCAL: exit %d:\n%s", code, stdout)
	}
}

func TestNamedProvidersFromConfig(t *testing.T) {
	scanned := filepath.Join("..", "..", "testdata", "detect", "handmade-scanned.pdf")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, fmt.Sprintf(`{"version": 1, "default": "mine", "providers": {
		"mine": {"command": %q, "env": {"DOCSTOMD_STUB": "markdown"}, "page_cost": 0.25},
		"other": {"command": %q, "env": {"DOCSTOMD_STUB": "lines"}, "local": true}
	}}`, exe, exe))
	code, stdout, stderr := runCLI(t, "convert", "--ocr", "auto", "--json", scanned)
	if code != exitOK || !strings.Contains(stdout, `"provider":"mine"`) || !strings.Contains(stdout, `"estimated_cost_usd":0.5`) || !strings.Contains(stdout, "page 1 of %PDF") {
		t.Errorf("config default: exit %d stderr %s:\n%s", code, stderr, stdout)
	}
	for _, spec := range []string{"other", "exec:other"} {
		code, stdout, stderr = runCLI(t, "convert", "--ocr", "auto", "--ocr-provider", spec, "--json", scanned)
		if code != exitOK || !strings.Contains(stdout, `"provider":"other"`) || !strings.Contains(stdout, `"local":true`) || !strings.Contains(stdout, "line on page 1") {
			t.Errorf("%s: exit %d stderr %s:\n%s", spec, code, stderr, stdout)
		}
	}
}

func TestBadConfigIsUsageError(t *testing.T) {
	scanned := filepath.Join("..", "..", "testdata", "detect", "handmade-scanned.pdf")
	for _, body := range []string{
		`{not json`,
		`{"version": 2}`,
		`{"version": 1, "providers": {"local": {"command": "x"}}}`,
		`{"version": 1, "providers": {"x": {}}}`,
	} {
		writeConfig(t, body)
		code, _, stderr := runCLI(t, "convert", "--ocr", "auto", scanned)
		if code != exitUsage || !strings.Contains(stderr, "providers.json") {
			t.Errorf("%s: exit %d stderr %s", body, code, stderr)
		}
	}
}

func TestMultiFileRunSharesOneEngineAndRunCap(t *testing.T) {
	scanned, err := os.ReadFile(filepath.Join("..", "..", "testdata", "detect", "handmade-scanned.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	in := t.TempDir()
	var files []string
	for _, name := range []string{"a.pdf", "b.pdf", "c.pdf"} {
		path := filepath.Join(in, name)
		if err := os.WriteFile(path, scanned, 0o600); err != nil {
			t.Fatal(err)
		}
		files = append(files, path)
	}
	// The stub counts its own starts in a file.
	starts := filepath.Join(t.TempDir(), "starts")
	t.Setenv(externaltest.EnvStarts, starts)
	out := t.TempDir()
	args := append([]string{"convert", "--ocr", "auto", "--ocr-provider", stubEngine(t, "markdown"), "--ocr-max-pages-run", "5", "--out-dir", out, "--json"}, files...)
	code, _, stderr := runCLI(t, args...)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if data, _ := os.ReadFile(starts); strings.Count(string(data), "start") != 1 {
		t.Errorf("engine starts = %q, want exactly one", data)
	}
	var billed []int
	for _, name := range []string{"a", "b", "c"} {
		data, err := os.ReadFile(filepath.Join(out, name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			OCRCost     docstomd.OCRCostReport `json:"ocr_cost"`
			NeedsReview []int                  `json:"needs_review"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		billed = append(billed, result.OCRCost.PagesBilled)
	}
	if fmt.Sprint(billed) != "[2 2 1]" {
		t.Errorf("pages per document = %v, want [2 2 1] under a 5-page run cap", billed)
	}
}

func TestMultiFileNeedsOutDir(t *testing.T) {
	code, _, _ := runCLI(t, "convert", "a.pdf", "b.pdf")
	if code != exitUsage {
		t.Errorf("exit %d, want usage", code)
	}
	code, _, stderr := runCLI(t, "convert", "--out-dir", t.TempDir(), "x/a.pdf", "y/a.pdf")
	if code != exitUsage || !strings.Contains(stderr, "both write") {
		t.Errorf("colliding outputs: exit %d %s", code, stderr)
	}
}

func TestOCRCommandDelegatesToLocalEngine(t *testing.T) {
	code, _, stderr := runCLI(t, "ocr", "list")
	if code != exitOCRUnavailable || !strings.Contains(stderr, "docstomd-ocr-local not found") {
		t.Errorf("no engine: exit %d stderr %q", code, stderr)
	}
	t.Setenv("DOCSTOMD_OCR_LOCAL", stubEngine(t, "args")[len("exec:"):])
	writeConfig(t, `{"version": 1, "catalogs": ["/etc/team-catalog.json"]}`)
	var stdout bytes.Buffer
	code = runOCR(context.Background(), []string{"install", "--accept-license", "CC-BY-4.0", "my-model"}, strings.NewReader(""), &stdout, io.Discard)
	if code != exitOK {
		t.Fatalf("exit %d", code)
	}
	var got []string
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, stdout.String())
	}
	want := []string{"install", "--catalog", "/etc/team-catalog.json", "--accept-license", "CC-BY-4.0", "my-model"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("engine args = %q, want %q", got, want)
	}
	if code, _, _ := runCLI(t, "ocr", "bogus"); code != exitUsage {
		t.Errorf("unknown subcommand exit %d", code)
	}
}

func TestLocalEngineArgs(t *testing.T) {
	cfg := &config{Catalogs: []string{"team.json"}}
	got := localArgs(providerFlags{model: "m", lang: "ar", backend: "go", catalogs: []string{"extra.json"}}, cfg)
	want := []string{"--model", "m", "--lang", "ar", "--backend", "go", "--catalog", "team.json", "--catalog", "extra.json"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("args = %q, want %q", got, want)
	}
}
