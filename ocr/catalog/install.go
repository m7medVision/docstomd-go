package catalog

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// ErrLicense means the model's licence must be accepted explicitly.
var ErrLicense = errors.New("licence not accepted")

// InstallOptions configures Install.
type InstallOptions struct {
	// Root is the models directory (default Root()).
	Root string
	// AcceptLicenses lists licence ids (or model ids) the user accepted.
	AcceptLicenses []string
	// Progress receives one line per file; nil is silent.
	Progress io.Writer
	// Client downloads files; nil uses a client with a generous timeout.
	Client *http.Client
}

// Install downloads e's files into <root>/<id>, verifying each against its
// SHA-256, and writes the manifest. Files land in a temporary directory that
// is renamed into place only when every file verified, so a failed install
// leaves no partial model behind. hf:// sources honour $HF_ENDPOINT; entries
// from user catalogs also send $HF_TOKEN to Hugging Face.
func Install(ctx context.Context, e *Entry, opts InstallOptions) error {
	if !e.IsPermissive() && !slices.Contains(opts.AcceptLicenses, e.License) && !slices.Contains(opts.AcceptLicenses, e.ID) {
		return fmt.Errorf("%w: %s is licensed %s; rerun with --accept-license %s after reading its terms", ErrLicense, e.ID, e.License, e.License)
	}
	root := opts.Root
	if root == "" {
		root = Root()
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(root, ".install-"+e.ID+"-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Minute}
	}
	for _, f := range e.Files {
		if opts.Progress != nil {
			fmt.Fprintf(opts.Progress, "%s: downloading %s (%s)\n", e.ID, f.Path, humanSize(f.Size))
		}
		if err := download(ctx, client, e, f, filepath.Join(tmp, filepath.FromSlash(f.Path))); err != nil {
			return fmt.Errorf("%s: %s: %w", e.ID, f.Path, err)
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, manifestFile), e.Manifest, 0o644); err != nil {
		return err
	}
	dest := filepath.Join(root, e.ID)
	old := ""
	if _, err := os.Stat(dest); err == nil {
		old = filepath.Join(root, ".old-"+e.ID+"-"+randomSuffix())
		if err := os.Rename(dest, old); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, dest); err != nil {
		if old != "" {
			_ = os.Rename(old, dest)
		}
		return err
	}
	if old != "" {
		_ = os.RemoveAll(old)
	}
	return nil
}

// ErrChecksum means a downloaded file does not match its catalog SHA-256.
var ErrChecksum = errors.New("checksum mismatch")

func download(ctx context.Context, client *http.Client, e *Entry, f File, dest string) error {
	url, err := sourceURL(f.Source, hfEndpoint())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if token := os.Getenv("HF_TOKEN"); token != "" && e.Origin != Builtin && strings.HasPrefix(f.Source, "hf://") {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	hash := sha256.New()
	// Read one byte past the declared size to catch oversized bodies.
	n, err := io.Copy(io.MultiWriter(out, hash), io.LimitReader(resp.Body, f.Size+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n != f.Size {
		return fmt.Errorf("%w: got %d bytes, want %d", ErrChecksum, n, f.Size)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != f.SHA256 {
		return fmt.Errorf("%w: sha256 %s, want %s", ErrChecksum, got, f.SHA256)
	}
	return nil
}

func randomSuffix() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// HumanSize formats a byte count for listings.
func HumanSize(n int64) string { return humanSize(n) }
