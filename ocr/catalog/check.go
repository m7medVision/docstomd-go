package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"strings"
)

// Check verifies e's pinned files without installing them. For hf://
// sources it compares the SHA-256 and size with the Hugging Face tree API
// (the LFS object id is the file's SHA-256); files stored without LFS and
// https:// sources are downloaded and hashed.
func Check(ctx context.Context, client *http.Client, e *Entry) error {
	if client == nil {
		client = http.DefaultClient
	}
	tmp, err := os.MkdirTemp("", "docstomd-catalog-check-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	for _, f := range e.Files {
		m := hfSource.FindStringSubmatch(f.Source)
		if m != nil {
			ok, err := checkTree(ctx, client, e, f, m[1], m[2], m[3])
			if err != nil {
				return fmt.Errorf("%s: %s: %w", e.ID, f.Path, err)
			}
			if ok {
				continue
			}
		}
		if err := download(ctx, client, e, f, path.Join(tmp, "file")); err != nil {
			return fmt.Errorf("%s: %s: %w", e.ID, f.Path, err)
		}
	}
	return nil
}

// checkTree compares f with the tree API entry; ok is false when the file is
// not stored in LFS and must be hashed.
func checkTree(ctx context.Context, client *http.Client, e *Entry, f File, repo, commit, file string) (bool, error) {
	endpoint := strings.TrimRight(hfEndpoint(), "/")
	if endpoint == "" {
		endpoint = "https://huggingface.co"
	}
	url := endpoint + "/api/models/" + repo + "/tree/" + commit
	if dir := path.Dir(file); dir != "." {
		url += "/" + dir
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	if token := os.Getenv("HF_TOKEN"); token != "" && e.Origin != Builtin {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	var tree []struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
		LFS  *struct {
			OID  string `json:"oid"`
			Size int64  `json:"size"`
		} `json:"lfs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tree); err != nil {
		return false, err
	}
	for _, t := range tree {
		if t.Path != file {
			continue
		}
		if t.LFS == nil {
			return false, nil
		}
		if t.LFS.OID != f.SHA256 || t.LFS.Size != f.Size {
			return false, fmt.Errorf("%w: Hugging Face has sha256 %s size %d, catalog says %s size %d", ErrChecksum, t.LFS.OID, t.LFS.Size, f.SHA256, f.Size)
		}
		return true, nil
	}
	return false, fmt.Errorf("%s is not in %s@%s", file, repo, commit)
}
