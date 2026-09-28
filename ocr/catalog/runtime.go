package catalog

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Runtime is a native inference library a fast backend loads at run time
// (never linked, never through cgo), pinned per platform.
type Runtime struct {
	// Name is the runtime family, e.g. "onnxruntime"; Backend is the
	// --backend value it serves.
	Name    string `json:"name"`
	Backend string `json:"backend"`
	Version string `json:"version"`
	// Platform is GOOS/GOARCH.
	Platform string `json:"platform"`
	License  string `json:"license"`
	// Archive is an https:// .tgz release asset.
	Archive File `json:"archive"`
	// Library is the archive member that becomes Library() once installed.
	Library string `json:"library"`
	// Extra members kept next to the library (licence notices).
	Extra []string `json:"extra,omitempty"`
}

// Dir is the runtime's install directory.
func (r *Runtime) Dir() string {
	return filepath.Join(RuntimesRoot(), r.Name+"-"+r.Version)
}

// LibraryPath is where the installed library lives.
func (r *Runtime) LibraryPath() string {
	return filepath.Join(r.Dir(), filepath.Base(r.Library))
}

// Installed reports whether the library is in place.
func (r *Runtime) Installed() bool {
	_, err := os.Stat(r.LibraryPath())
	return err == nil
}

// EnvRuntimes overrides where runtimes are installed.
const EnvRuntimes = "DOCSTOMD_OCR_RUNTIMES"

// RuntimesRoot is $DOCSTOMD_OCR_RUNTIMES, else <user data dir>/docstomd/ocr/runtimes.
func RuntimesRoot() string {
	if dir := os.Getenv(EnvRuntimes); dir != "" {
		return dir
	}
	return filepath.Join(dataDir(), "docstomd", "ocr", "runtimes")
}

// ErrNoRuntime means no pinned runtime exists for this platform.
var ErrNoRuntime = errors.New("no runtime for this platform")

// RuntimeFor returns the built-in runtime serving backend on this platform.
func RuntimeFor(backend string) (*Runtime, error) {
	platform := runtime.GOOS + "/" + runtime.GOARCH
	runtimes := Load().Runtimes
	for i := range runtimes {
		r := &runtimes[i]
		if r.Backend == backend && r.Platform == platform {
			return r, nil
		}
	}
	return nil, fmt.Errorf("%w: the %s backend has no pinned runtime for %s", ErrNoRuntime, backend, platform)
}

// maxRuntimeMember bounds one extracted file.
const maxRuntimeMember = 512 << 20

// InstallRuntime downloads r's archive, verifies its SHA-256 and extracts
// the library and notices into Dir, atomically.
func InstallRuntime(ctx context.Context, r *Runtime, progress io.Writer) error {
	if err := os.MkdirAll(RuntimesRoot(), 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(RuntimesRoot(), ".install-"+r.Name+"-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if progress != nil {
		fmt.Fprintf(progress, "%s %s: downloading %s (%s)\n", r.Name, r.Version, r.Archive.Source, humanSize(r.Archive.Size))
	}
	archive := filepath.Join(tmp, "archive.tgz")
	e := &Entry{ID: r.Name, Origin: Builtin}
	client := &http.Client{Timeout: 30 * time.Minute}
	if err := download(ctx, client, e, r.Archive, archive); err != nil {
		return fmt.Errorf("%s: %w", r.Name, err)
	}
	dest := filepath.Join(tmp, "out")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	want := map[string]bool{r.Library: true}
	for _, m := range r.Extra {
		want[m] = true
	}
	if err := extract(archive, dest, want); err != nil {
		return fmt.Errorf("%s: %w", r.Name, err)
	}
	if err := os.RemoveAll(r.Dir()); err != nil {
		return err
	}
	return os.Rename(dest, r.Dir())
}

// extract copies the wanted regular-file members of a .tgz into dir, by base
// name; every wanted member must be present.
func extract(archive, dir string, want map[string]bool) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	found := map[string]bool{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.ToSlash(filepath.Clean(h.Name))
		if !want[name] || h.Typeflag != tar.TypeReg {
			continue
		}
		out, err := os.OpenFile(filepath.Join(dir, filepath.Base(name)), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		n, err := io.Copy(out, io.LimitReader(tr, maxRuntimeMember+1))
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		if n > maxRuntimeMember {
			return fmt.Errorf("%s is larger than %d bytes", name, maxRuntimeMember)
		}
		found[name] = true
	}
	for name := range want {
		if !found[name] {
			return fmt.Errorf("archive has no regular file %s", name)
		}
	}
	return nil
}
