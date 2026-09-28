package pageimage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"testing"
)

// rotatedPDF is a 200×100 pt page with a black 20×20 pt square at the top
// left of the unrotated page, displayed with /Rotate rotate.
func rotatedPDF(rotate int) []byte {
	content := "0 g 10 70 20 20 re f"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Kids [3 0 R] /Count 1 /Rotate %d >>", rotate),
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 100] /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
	}
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

func TestRenderUndoesRotation(t *testing.T) {
	if _, err := exec.LookPath(Renderer); err != nil {
		t.Skip("pdftoppm not installed")
	}
	for _, rotate := range []int{0, 90, 180, 270} {
		t.Run(fmt.Sprint(rotate), func(t *testing.T) {
			pdf := rotatedPDF(rotate)
			doc, err := Open(pdf)
			if err != nil {
				t.Fatal(err)
			}
			page, err := doc.Render(context.Background(), pdf, 1, 72, "")
			if err != nil {
				t.Fatal(err)
			}
			b := page.Image.Bounds()
			if b.Dx() != 200 || b.Dy() != 100 || page.Placement != (Rect{0, 0, 200, 100}) {
				t.Fatalf("image %v placement %+v, want 200×100 over the page", b, page.Placement)
			}
			// The square spans x 10..30, y 10..30 from the top-left.
			for _, p := range [][2]int{{20, 20}, {180, 20}, {20, 80}, {180, 80}} {
				r, _, _, _ := page.Image.At(p[0], p[1]).RGBA()
				want := p == [2]int{20, 20}
				if dark := r < 0x8000; dark != want {
					t.Errorf("pixel %v dark=%v, want %v", p, dark, want)
				}
			}
		})
	}
}

func TestRenderWithoutPdftoppm(t *testing.T) {
	t.Setenv("PATH", "")
	pdf := rotatedPDF(0)
	doc, err := Open(pdf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := doc.Render(context.Background(), pdf, 1, 72, ""); !errors.Is(err, ErrNoRenderer) {
		t.Errorf("err = %v, want ErrNoRenderer", err)
	}
}

func TestUndecodableEncodingsAreNoImage(t *testing.T) {
	for _, name := range []string{"field-report-ccitt.pdf", "field-report-vector.pdf"} {
		doc, err := Open(fixture(t, name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := doc.Extract(1); !errors.Is(err, ErrNoImage) {
			t.Errorf("%s: err = %v, want ErrNoImage", name, err)
		}
	}
}
