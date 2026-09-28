package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/m7medVision/docstomd-go/ocr/internal/ocrtest"
)

// session runs the server in-process on pipes and returns a request
// function plus the handshake.
func session(t *testing.T, args ...string) (func(req map[string]any) map[string]any, map[string]any) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- run(context.Background(), args, inR, outW, &stderr)
		outW.Close()
	}()
	t.Cleanup(func() {
		inW.Close()
		<-done
	})
	out := bufio.NewReader(outR)
	read := func() map[string]any {
		line, err := out.ReadBytes('\n')
		if err != nil {
			t.Fatalf("read: %v (stderr: %s)", err, stderr.String())
		}
		var msg map[string]any
		if err := json.Unmarshal(line, &msg); err != nil {
			t.Fatalf("bad line %q", line)
		}
		return msg
	}
	hello := read()
	return func(req map[string]any) map[string]any {
		b, _ := json.Marshal(req)
		inW.Write(append(b, '\n'))
		return read()
	}, hello
}

func TestHandshake(t *testing.T) {
	_, hello := session(t, "--models", t.TempDir())
	if hello["type"] != "hello" || hello["protocol"] != float64(1) || hello["engine"] != "docstomd-ocr-local" || hello["local"] != true {
		t.Errorf("hello = %v", hello)
	}
}

func TestNoModelIsUnavailableWithHint(t *testing.T) {
	send, _ := session(t, "--models", t.TempDir())
	pdf := base64.StdEncoding.EncodeToString(ocrtest.Fixture(t, "field-report-jpeg.pdf"))
	resp := send(map[string]any{"type": "recognize", "id": "1", "pdf": pdf, "pages": []int{1}})
	msg, _ := resp["message"].(string)
	if resp["type"] != "error" || resp["code"] != "unavailable" || !strings.Contains(msg, "docstomd ocr install pp-ocrv5-mobile") {
		t.Errorf("resp = %v", resp)
	}
	resp = send(map[string]any{"type": "recognize", "id": "2", "pdf": pdf, "pages": []int{1}, "x_future": true})
	if resp["type"] != "error" || resp["id"] != "2" {
		t.Errorf("second request: %v", resp)
	}
	root := t.TempDir()
	send, _ = session(t, "--models", root, "--model", "nope")
	resp = send(map[string]any{"type": "recognize", "id": "1", "pdf": pdf, "pages": []int{1}})
	if msg, _ := resp["message"].(string); resp["code"] != "unavailable" || !strings.Contains(msg, "docstomd ocr install nope") {
		t.Errorf("unknown model: %v", resp)
	}
}

func TestRecognizeReturnsPageLines(t *testing.T) {
	dir := ocrtest.ModelDir(t, "pp-ocrv5-mobile")
	send, _ := session(t, "--model-dir", dir, "--backend", ocrtest.Backend())
	pdf := base64.StdEncoding.EncodeToString(ocrtest.Fixture(t, "field-report-flate.pdf"))
	resp := send(map[string]any{"type": "recognize", "id": "7", "pdf": pdf, "pages": []int{1}})
	pages, _ := resp["pages"].([]any)
	if resp["type"] != "result" || resp["id"] != "7" || len(pages) != 1 {
		t.Fatalf("resp = %v", resp)
	}
	page := pages[0].(map[string]any)
	if page["width"] != float64(360) || page["height"] != float64(270) {
		t.Errorf("page space %v×%v, want the 360×270 pt page", page["width"], page["height"])
	}
	lines := page["lines"].([]any)
	first := lines[0].(map[string]any)
	box := first["box"].(map[string]any)
	// Golden box 81,76 px on a 1000 px wide image of a 360 pt page.
	if first["text"] != "Quarterly Field Report" || box["x0"].(float64) < 26 || box["x0"].(float64) > 32 || box["y0"].(float64) < 24 || box["y0"].(float64) > 30 {
		t.Errorf("first line = %v", first)
	}
}

// TestConvertEndToEnd builds the docstomd CLI from this repository and this
// engine, then converts the scanned fixtures through --ocr-provider local.
func TestConvertEndToEnd(t *testing.T) {
	dir := ocrtest.ModelDir(t, "pp-ocrv5-mobile")
	bin := t.TempDir()
	build := func(pkgDir, out string) {
		cmd := exec.Command("go", "build", "-o", filepath.Join(bin, out), ".")
		cmd.Dir = pkgDir
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("building %s: %v\n%s", out, err, b)
		}
	}
	build(filepath.Join(ocrtest.RepoRoot(), "cmd", "docstomd"), "docstomd")
	build(".", "docstomd-ocr-local")
	models := t.TempDir()
	if err := os.Symlink(dir, filepath.Join(models, "pp-ocrv5-mobile")); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []string{"field-report-jpeg.pdf", "field-report-flate.pdf"} {
		cmd := exec.Command(filepath.Join(bin, "docstomd"), "convert", "--ocr", "auto", "--ocr-provider", "local", "--json",
			filepath.Join(ocrtest.RepoRoot(), "testdata", "ocr", fixture))
		// The engine is found next to the docstomd binary.
		cmd.Env = append(os.Environ(), "DOCSTOMD_OCR_MODELS="+models, "XDG_CONFIG_HOME="+t.TempDir())
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s: %v\n%s%s", fixture, err, out, stderr.String())
		}
		var result struct {
			Markdown string `json:"markdown"`
			OCRCost  struct {
				Provider string `json:"provider"`
				Local    bool   `json:"local"`
			} `json:"ocr_cost"`
			NeedsReview []int `json:"needs_review"`
		}
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"# Quarterly Field Report", "42 clear days", "- Replace the wind sensor", "- Calibrate the rain gauge", "1,250 USD"} {
			if !strings.Contains(result.Markdown, want) {
				t.Errorf("%s: markdown lacks %q:\n%s", fixture, want, result.Markdown)
			}
		}
		if result.OCRCost.Provider != "local" || !result.OCRCost.Local || len(result.NeedsReview) != 0 {
			t.Errorf("%s: cost %+v needs_review %v", fixture, result.OCRCost, result.NeedsReview)
		}
	}
	// A CCITT page with no renderer installed: the document still converts
	// and the page is flagged for review.
	cmd := exec.Command(filepath.Join(bin, "docstomd"), "convert", "--ocr", "auto", "--ocr-provider", "local", "--json",
		filepath.Join(ocrtest.RepoRoot(), "testdata", "ocr", "field-report-ccitt.pdf"))
	cmd.Env = append(os.Environ(), "DOCSTOMD_OCR_MODELS="+models, "XDG_CONFIG_HOME="+t.TempDir(), "PATH=")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ccitt without pdftoppm: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), `"needs_review":[1]`) {
		t.Errorf("ccitt without pdftoppm: want needs_review [1]:\n%s", out)
	}
}

func TestInstalledModelsWorkOffline(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "pp-ocrv5-mobile")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"schema":1,"id":"pp-ocrv5-mobile","languages":["en"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	// An unreachable user catalog must not stop an installed model.
	got, err := resolveModel(context.Background(), options{modelsRoot: root, lang: "en", catalogs: stringList{"https://127.0.0.1:1/catalog.json"}}, &stderr)
	if err != nil || got != dir {
		t.Errorf("resolveModel = %q, %v", got, err)
	}
	if !strings.Contains(stderr.String(), "built-in catalog") {
		t.Errorf("no warning about the unreachable catalog: %q", stderr.String())
	}
}

func TestListShowsCatalogAndInstalled(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pp-ocrv5-mobile"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pp-ocrv5-mobile", "manifest.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"list", "--json", "--models", root}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	var entries []struct {
		ID        string `json:"id"`
		Installed bool   `json:"installed"`
		License   string `json:"license"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 || entries[0].ID != "pp-ocrv5-mobile" || !entries[0].Installed || entries[1].Installed || entries[0].License != "Apache-2.0" {
		t.Errorf("list = %+v", entries)
	}
}

// TestPagesWithoutImages: CCITT and vector-drawn pages are rendered with
// pdftoppm when it is installed; without it they stay unanswered, which
// docstomd turns into needs_review with the native text kept.
func TestPagesWithoutImages(t *testing.T) {
	dir := ocrtest.ModelDir(t, "pp-ocrv5-mobile")
	want := ocrtest.Golden(t, "field-report")
	for _, fixture := range []string{"field-report-ccitt.pdf", "field-report-vector.pdf"} {
		pdf := base64.StdEncoding.EncodeToString(ocrtest.Fixture(t, fixture))
		t.Run(fixture+"/pdftoppm", func(t *testing.T) {
			if _, err := exec.LookPath("pdftoppm"); err != nil {
				t.Skip("pdftoppm not installed")
			}
			send, _ := session(t, "--model-dir", dir, "--backend", ocrtest.Backend())
			resp := send(map[string]any{"type": "recognize", "id": "1", "pdf": pdf, "pages": []int{1}})
			pages, _ := resp["pages"].([]any)
			if len(pages) != 1 {
				t.Fatalf("resp = %v", resp)
			}
			var got []string
			for _, l := range pages[0].(map[string]any)["lines"].([]any) {
				got = append(got, strings.ReplaceAll(l.(map[string]any)["text"].(string), " ", ""))
			}
			for _, w := range want {
				found := false
				for _, g := range got {
					found = found || g == strings.ReplaceAll(w.Text, " ", "")
				}
				if !found {
					t.Errorf("missing line %q in %q", w.Text, got)
				}
			}
		})
		t.Run(fixture+"/no-renderer", func(t *testing.T) {
			t.Setenv("PATH", "")
			send, _ := session(t, "--model-dir", dir, "--backend", ocrtest.Backend())
			resp := send(map[string]any{"type": "recognize", "id": "1", "pdf": pdf, "pages": []int{1}})
			if pages, _ := resp["pages"].([]any); resp["type"] != "result" || len(pages) != 0 {
				t.Errorf("resp = %v, want a result with the page unanswered", resp)
			}
		})
	}
}
