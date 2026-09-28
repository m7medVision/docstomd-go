package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

func TestBuiltinCatalogIsPinnedAndPermissive(t *testing.T) {
	c := Load()
	if len(c.Models) < 2 {
		t.Fatalf("builtin catalog has %d models", len(c.Models))
	}
	for _, e := range c.Models {
		if !e.IsPermissive() {
			t.Errorf("%s: builtin catalog lists a %s model; only %v belong here", e.ID, e.License, Permissive)
		}
		for _, f := range e.Files {
			if !hfSource.MatchString(f.Source) {
				t.Errorf("%s: %s: builtin sources must be pinned hf:// URLs, got %s", e.ID, f.Path, f.Source)
			}
		}
	}
	for _, want := range []string{"pp-ocrv5-mobile", "pp-ocrv5-server"} {
		if _, ok := (Set{c}).Find(want); !ok {
			t.Errorf("builtin catalog lacks %s", want)
		}
	}
	if c.Models[0].ID != DefaultModel {
		t.Errorf("first builtin model is %s, want the default %s", c.Models[0].ID, DefaultModel)
	}
}

func TestSourceURL(t *testing.T) {
	commit := strings.Repeat("a", 40)
	got, err := sourceURL("hf://Org/repo.x@"+commit+"/dir/model.onnx", "")
	if err != nil || got != "https://huggingface.co/Org/repo.x/resolve/"+commit+"/dir/model.onnx" {
		t.Errorf("hf source = %q, %v", got, err)
	}
	got, _ = sourceURL("hf://Org/repo@"+commit+"/m.onnx", "https://mirror.example/")
	if got != "https://mirror.example/Org/repo/resolve/"+commit+"/m.onnx" {
		t.Errorf("HF_ENDPOINT source = %q", got)
	}
	for _, bad := range []string{"hf://Org/repo@main/m.onnx", "hf://repo@" + commit + "/m", "http://x/y", "file:///etc/passwd", "ftp://x"} {
		if _, err := sourceURL(bad, ""); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

// fakeHF serves files under /<repo>/resolve/<commit>/<file> and a tree API,
// recording Authorization headers.
type fakeHF struct {
	*httptest.Server
	files map[string][]byte // "<repo>/<file>" → content
	mu    sync.Mutex
	auth  []string
}

func newFakeHF(t *testing.T, files map[string][]byte) *fakeHF {
	f := &fakeHF{files: files}
	resolve := regexp.MustCompile(`^/([^/]+/[^/]+)/resolve/[0-9a-f]{40}/(.+)$`)
	tree := regexp.MustCompile(`^/api/models/([^/]+/[^/]+)/tree/[0-9a-f]{40}$`)
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.mu.Unlock()
		if m := resolve.FindStringSubmatch(r.URL.Path); m != nil {
			if data, ok := f.files[m[1]+"/"+m[2]]; ok {
				_, _ = w.Write(data)
				return
			}
		}
		if m := tree.FindStringSubmatch(r.URL.Path); m != nil {
			var out []map[string]any
			for key, data := range f.files {
				repo, file, _ := strings.Cut(key[strings.Index(key, "/")+1:], "/")
				if strings.HasPrefix(key, m[1]+"/") {
					_ = repo
					sum := sha256.Sum256(data)
					out = append(out, map[string]any{"path": file, "size": len(data), "lfs": map[string]any{"oid": hex.EncodeToString(sum[:]), "size": len(data)}})
				}
			}
			_ = json.NewEncoder(w).Encode(out)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(f.Close)
	t.Setenv("HF_ENDPOINT", f.URL)
	return f
}

const commit = "0123456789abcdef0123456789abcdef01234567"

// testEntry is a tiny model whose manifest refers to its files.
func testEntry(t *testing.T, id, license string, files map[string][]byte, corrupt string) *Entry {
	t.Helper()
	manifest := fmt.Sprintf(`{"schema":1,"id":%q,"languages":["en"],"detector":{"model":"det.onnx","limit_side":960,"input":{"scale":1,"mean":[0,0,0],"std":[1,1,1]},"postprocess":{"type":"db","thresh":0.3,"box_thresh":0.6,"unclip_ratio":1.5}},"recognizer":{"model":"rec/rec.onnx","height":48,"input":{"scale":1,"mean":[0,0,0],"std":[1,1,1]},"charset":{"file":"rec/chars.txt"},"decoder":{"type":"ctc"}}}`, id)
	e := &Entry{ID: id, License: license, Languages: []string{"en"}, Manifest: json.RawMessage(manifest)}
	for _, path := range []string{"det.onnx", "rec/rec.onnx", "rec/chars.txt"} {
		data := files["org/"+id+"/"+filepath.Base(path)]
		sum := sha256.Sum256(data)
		if path == corrupt {
			sum[0] ^= 0xff
		}
		e.Files = append(e.Files, File{Path: path, Source: "hf://org/" + id + "@" + commit + "/" + filepath.Base(path), SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))})
	}
	if err := e.validate(); err != nil {
		t.Fatal(err)
	}
	return e
}

func modelFiles(id string) map[string][]byte {
	return map[string][]byte{
		"org/" + id + "/det.onnx":  []byte("detector weights " + id),
		"org/" + id + "/rec.onnx":  []byte("recognizer weights " + id),
		"org/" + id + "/chars.txt": []byte("a\nb\n"),
	}
}

func TestInstallVerifiesAndWritesManifest(t *testing.T) {
	files := modelFiles("tiny")
	newFakeHF(t, files)
	root := t.TempDir()
	e := testEntry(t, "tiny", "MIT", files, "")
	e.Origin = Builtin
	if err := Install(context.Background(), e, InstallOptions{Root: root}); err != nil {
		t.Fatal(err)
	}
	for path, key := range map[string]string{"det.onnx": "det.onnx", "rec/rec.onnx": "rec.onnx", "rec/chars.txt": "chars.txt"} {
		got, err := os.ReadFile(filepath.Join(root, "tiny", path))
		if err != nil || string(got) != string(files["org/tiny/"+key]) {
			t.Errorf("%s = %q, %v", path, got, err)
		}
	}
	if got := Installed(root); len(got) != 1 || got[0] != "tiny" {
		t.Errorf("installed = %v", got)
	}
	if _, err := os.Stat(filepath.Join(root, "tiny", "manifest.json")); err != nil {
		t.Error(err)
	}
	// Reinstalling replaces the model in place.
	if err := Install(context.Background(), e, InstallOptions{Root: root}); err != nil {
		t.Fatal(err)
	}
	assertOnly(t, root, "tiny")
}

func TestChecksumMismatchLeavesNothing(t *testing.T) {
	files := modelFiles("tiny")
	newFakeHF(t, files)
	root := t.TempDir()
	e := testEntry(t, "tiny", "MIT", files, "rec/rec.onnx")
	err := Install(context.Background(), e, InstallOptions{Root: root})
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("err = %v, want checksum mismatch", err)
	}
	assertOnly(t, root)
	// A body longer than the catalog size is rejected too.
	e = testEntry(t, "tiny", "MIT", files, "")
	e.Files[0].Size--
	if err := Install(context.Background(), e, InstallOptions{Root: root}); !errors.Is(err, ErrChecksum) {
		t.Errorf("oversized body: err = %v", err)
	}
	assertOnly(t, root)
}

func assertOnly(t *testing.T, root string, names ...string) {
	t.Helper()
	entries, _ := os.ReadDir(root)
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if fmt.Sprint(got) != fmt.Sprint(names) {
		t.Errorf("models dir holds %v, want %v", got, names)
	}
}

func TestRestrictedLicenceNeedsAcceptance(t *testing.T) {
	files := modelFiles("nc")
	newFakeHF(t, files)
	root := t.TempDir()
	e := testEntry(t, "nc", "CC-BY-NC-4.0", files, "")
	err := Install(context.Background(), e, InstallOptions{Root: root})
	if !errors.Is(err, ErrLicense) || !strings.Contains(err.Error(), "--accept-license CC-BY-NC-4.0") {
		t.Fatalf("err = %v", err)
	}
	assertOnly(t, root)
	for _, accept := range []string{"CC-BY-NC-4.0", "nc"} {
		if err := Install(context.Background(), e, InstallOptions{Root: root, AcceptLicenses: []string{accept}}); err != nil {
			t.Errorf("accepted %s: %v", accept, err)
		}
	}
}

func TestHFTokenOnlyForUserCatalogs(t *testing.T) {
	files := modelFiles("tiny")
	hf := newFakeHF(t, files)
	t.Setenv("HF_TOKEN", "secret")
	e := testEntry(t, "tiny", "MIT", files, "")
	e.Origin = Builtin
	if err := Install(context.Background(), e, InstallOptions{Root: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	e.Origin = "/home/me/catalog.json"
	if err := Install(context.Background(), e, InstallOptions{Root: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	want := []string{"", "", "", "Bearer secret", "Bearer secret", "Bearer secret"}
	if fmt.Sprint(hf.auth) != fmt.Sprint(want) {
		t.Errorf("authorization headers = %q, want %q", hf.auth, want)
	}
}

func TestCheckAgainstTreeAPI(t *testing.T) {
	files := modelFiles("tiny")
	newFakeHF(t, files)
	e := testEntry(t, "tiny", "MIT", files, "")
	if err := Check(context.Background(), nil, e); err != nil {
		t.Errorf("check: %v", err)
	}
	e = testEntry(t, "tiny", "MIT", files, "det.onnx")
	if err := Check(context.Background(), nil, e); !errors.Is(err, ErrChecksum) {
		t.Errorf("tampered pin: err = %v", err)
	}
}

func TestParseRejectsBadCatalogs(t *testing.T) {
	good := testEntry(t, "tiny", "MIT", modelFiles("tiny"), "")
	body := func(mutate func(map[string]any)) []byte {
		raw, _ := json.Marshal(good)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		mutate(m)
		out, _ := json.Marshal(map[string]any{"catalog": 1, "models": []any{m}})
		return out
	}
	if _, err := Parse(body(func(map[string]any) {}), "user.json"); err != nil {
		t.Fatalf("good catalog: %v", err)
	}
	cases := map[string]func(map[string]any){
		"traversal":   func(m map[string]any) { m["files"].([]any)[0].(map[string]any)["path"] = "../evil" },
		"no sha":      func(m map[string]any) { m["files"].([]any)[0].(map[string]any)["sha256"] = "abc" },
		"http source": func(m map[string]any) { m["files"].([]any)[0].(map[string]any)["source"] = "http://x/y" },
		"unpinned hf": func(m map[string]any) {
			m["files"].([]any)[0].(map[string]any)["source"] = "hf://org/tiny@main/det.onnx"
		},
		"missing file": func(m map[string]any) {
			m["files"] = m["files"].([]any)[1:]
		},
		"bad id": func(m map[string]any) { m["id"] = "Bad ID" },
	}
	for name, mutate := range cases {
		if _, err := Parse(body(mutate), "user.json"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Parse([]byte(`{"catalog": 2, "models": []}`), "user.json"); err == nil {
		t.Error("format 2 accepted")
	}
}

func TestSelect(t *testing.T) {
	root := t.TempDir()
	write := func(id string, langs ...string) {
		dir := filepath.Join(root, id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		m, _ := json.Marshal(map[string]any{"schema": 1, "id": id, "languages": langs})
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), m, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	set := Set{Load()}
	if _, err := Select(set, root, "", ""); !errors.Is(err, ErrNotInstalled) || !strings.Contains(err.Error(), "docstomd ocr install pp-ocrv5-mobile") {
		t.Errorf("nothing installed: %v", err)
	}
	write("zzz-custom", "de")
	if dir, _ := Select(set, root, "", ""); filepath.Base(dir) != "zzz-custom" {
		t.Errorf("only installed model: %s", dir)
	}
	write("pp-ocrv5-server", "zh", "en")
	write("pp-ocrv5-mobile", "zh", "en")
	if dir, _ := Select(set, root, "", ""); filepath.Base(dir) != "pp-ocrv5-mobile" {
		t.Errorf("catalog order: %s", dir)
	}
	if dir, _ := Select(set, root, "pp-ocrv5-server", ""); filepath.Base(dir) != "pp-ocrv5-server" {
		t.Errorf("--model: %s", dir)
	}
	if dir, _ := Select(set, root, "", "de"); filepath.Base(dir) != "zzz-custom" {
		t.Errorf("--lang de: %s", dir)
	}
	if _, err := Select(set, root, "", "ar"); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("--lang ar: %v", err)
	}
	if _, err := Select(set, root, "nope", ""); !errors.Is(err, ErrNotInstalled) || !strings.Contains(err.Error(), "install nope") {
		t.Errorf("--model nope: %v", err)
	}
}

func TestBuiltinRuntimesArePinned(t *testing.T) {
	c := Load()
	if len(c.Runtimes) == 0 {
		t.Fatal("no runtimes")
	}
	for _, r := range c.Runtimes {
		if r.Backend != "onnx" || !hexSHA256.MatchString(r.Archive.SHA256) || r.Archive.Size <= 0 || !strings.HasPrefix(r.Archive.Source, "https://github.com/microsoft/onnxruntime/releases/download/") {
			t.Errorf("runtime %s %s: bad pin %+v", r.Name, r.Platform, r.Archive)
		}
		if !slicesContains(Permissive, r.License) {
			t.Errorf("runtime %s: licence %s", r.Platform, r.License)
		}
	}
}

func slicesContains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
