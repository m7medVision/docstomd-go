package pageimage

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// ErrNoRenderer means pdftoppm (poppler) is not installed.
var ErrNoRenderer = errors.New("pageimage: pdftoppm is not installed")

// Renderer is the external program Render runs.
const Renderer = "pdftoppm"

// Render rasterizes 1-indexed page n of pdf with pdftoppm at dpi, for pages
// whose image cannot be extracted in pure Go (CCITT, JBIG2, JPEG 2000,
// vector-drawn text). The image covers the visible page and is turned back
// by the page's /Rotate, so it lives in the same page space as Extract.
func (d *Document) Render(ctx context.Context, pdf []byte, n, dpi int, password string) (*Page, error) {
	bin, err := exec.LookPath(Renderer)
	if err != nil {
		return nil, ErrNoRenderer
	}
	pages := d.doc.Pages()
	if n < 1 || n > len(pages) {
		return nil, fmt.Errorf("pageimage: page %d out of range 1..%d", n, len(pages))
	}
	pageDict := d.doc.PageDict(pages[n-1])
	if pageDict == nil {
		return nil, ErrNoImage
	}
	box := d.pageBox(pageDict)
	dir, err := os.MkdirTemp("", "docstomd-render-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	in := filepath.Join(dir, "in.pdf")
	if err := os.WriteFile(in, pdf, 0o600); err != nil {
		return nil, err
	}
	args := []string{"-r", strconv.Itoa(dpi), "-gray", "-png", "-cropbox", "-singlefile", "-f", strconv.Itoa(n), "-l", strconv.Itoa(n)}
	if password != "" {
		args = append(args, "-upw", password)
	}
	args = append(args, in, filepath.Join(dir, "page"))
	if out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("pdftoppm page %d: %v: %s", n, err, out)
	}
	f, err := os.Open(filepath.Join(dir, "page.png"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	cfg, err := png.DecodeConfig(f)
	if err != nil || cfg.Width*cfg.Height > maxPixels {
		return nil, fmt.Errorf("%w: rendered page too large or unreadable", ErrNoImage)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	img, err := png.Decode(f)
	if err != nil {
		return nil, err
	}
	img = unrotate(img, d.rotation(pageDict))
	w, h := box.x1-box.x0, box.y1-box.y0
	return &Page{Image: img, Width: w, Height: h, Placement: Rect{0, 0, w, h}}, nil
}

// rotation is the page's /Rotate (inherited), normalized to 0, 90, 180 or
// 270.
func (d *Document) rotation(pageDict map[string]any) int {
	node := pageDict
	for range 64 {
		if node == nil {
			break
		}
		if v, ok := number(d.doc.Resolve(node["Rotate"])); ok {
			r := int(v) % 360
			if r < 0 {
				r += 360
			}
			return r / 90 * 90
		}
		parent, ok := d.doc.Resolve(node["Parent"]).(map[string]any)
		if !ok {
			break
		}
		node = parent
	}
	return 0
}

// unrotate turns a page rendered with /Rotate applied back into unrotated
// page space. /Rotate turns the page clockwise for display.
func unrotate(img image.Image, rotate int) image.Image {
	switch rotate {
	case 90:
		// Displayed pixel (H-1-y, x) shows page pixel (x, y).
		return transform(img, false, true, true)
	case 180:
		return transform(img, true, true, false)
	case 270:
		// Displayed pixel (y, W-1-x) shows page pixel (x, y).
		return transform(img, true, false, true)
	}
	return img
}
