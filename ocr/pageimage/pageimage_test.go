package pageimage

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "ocr", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestExtractScannedFixtures(t *testing.T) {
	var images []image.Image
	for _, name := range []string{"field-report-jpeg.pdf", "field-report-flate.pdf"} {
		doc, err := Open(fixture(t, name))
		if err != nil {
			t.Fatal(err)
		}
		page, err := doc.Extract(1)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if b := page.Image.Bounds(); b.Dx() != 1000 || b.Dy() != 750 {
			t.Errorf("%s: image %v, want 1000x750", name, b)
		}
		if page.Width != 360 || page.Height != 270 || page.Placement != (Rect{0, 0, 360, 270}) {
			t.Errorf("%s: page %vx%v placement %+v", name, page.Width, page.Height, page.Placement)
		}
		images = append(images, page.Image)
	}
	// JPEG and Flate carry the same raster; the title's first dark pixel row
	// must agree.
	if a, b := firstDarkRow(images[0]), firstDarkRow(images[1]); a < 0 || abs(a-b) > 2 {
		t.Errorf("first dark rows differ: jpeg %d flate %d", a, b)
	}
}

func firstDarkRow(img image.Image) int {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if r, _, _, _ := img.At(x, y).RGBA(); r < 0x4000 {
				return y
			}
		}
	}
	return -1
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// markerPDF paints a 4x2 gray image whose top-left pixel is black, with the
// given content stream, optionally wrapped in a form XObject.
func markerPDF(t *testing.T, content string, form bool) []byte {
	t.Helper()
	pix := []byte{0, 255, 255, 255, 255, 255, 255, 255}
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(pix)
	zw.Close()
	image := fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width 4 /Height 2 /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", z.Len(), z.Bytes())
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 100] /Contents 4 0 R /Resources << /XObject << /Im1 5 0 R /Fm1 6 0 R >> >> >>",
	}
	page := content
	if form {
		page = "q 1 0 0 1 10 0 cm /Fm1 Do Q"
	}
	objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(page), page), image,
		fmt.Sprintf("<< /Type /XObject /Subtype /Form /BBox [0 0 200 100] /Resources << /XObject << /Im1 5 0 R >> >> /Length %d >>\nstream\n%s\nendstream", len(content), content))
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for i, obj := range objects {
		offsets[i] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return buf.Bytes()
}

func TestPlacementAndOrientation(t *testing.T) {
	cases := []struct {
		name    string
		content string
		form    bool
		w, h    int
		black   image.Point
		place   Rect
	}{
		{"upright", "q 80 0 0 40 20 10 cm /Im1 Do Q", false, 4, 2, image.Pt(0, 0), Rect{20, 50, 100, 90}},
		{"flipped vertically", "q 80 0 0 -40 20 50 cm /Im1 Do Q", false, 4, 2, image.Pt(0, 1), Rect{20, 50, 100, 90}},
		{"mirrored", "q -80 0 0 40 100 10 cm /Im1 Do Q", false, 4, 2, image.Pt(3, 0), Rect{20, 50, 100, 90}},
		// Rotated 90° counter-clockwise: image x runs up the page.
		{"rotated", "q 0 80 -40 0 60 10 cm /Im1 Do Q", false, 2, 4, image.Pt(0, 3), Rect{20, 10, 60, 90}},
		{"form", "q 80 0 0 40 20 10 cm /Im1 Do Q", true, 4, 2, image.Pt(0, 0), Rect{30, 50, 110, 90}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := Open(markerPDF(t, tc.content, tc.form))
			if err != nil {
				t.Fatal(err)
			}
			page, err := doc.Extract(1)
			if err != nil {
				t.Fatal(err)
			}
			b := page.Image.Bounds()
			if b.Dx() != tc.w || b.Dy() != tc.h {
				t.Fatalf("image %v, want %dx%d", b, tc.w, tc.h)
			}
			for y := range tc.h {
				for x := range tc.w {
					r, _, _, _ := page.Image.At(x, y).RGBA()
					if dark := r < 0x8000; dark != (image.Pt(x, y) == tc.black) {
						t.Errorf("pixel (%d,%d) dark=%v, want black only at %v", x, y, dark, tc.black)
					}
				}
			}
			if page.Placement != tc.place {
				t.Errorf("placement %+v, want %+v", page.Placement, tc.place)
			}
		})
	}
}

func TestNoImage(t *testing.T) {
	doc, err := Open(markerPDF(t, "BT ET", false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := doc.Extract(1); !errors.Is(err, ErrNoImage) {
		t.Errorf("err = %v, want ErrNoImage", err)
	}
	if _, err := doc.Extract(2); err == nil {
		t.Error("page 2 of 1 must fail")
	}
}
