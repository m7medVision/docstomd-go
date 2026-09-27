// Command ocrfixtures regenerates the scanned-page fixtures in testdata/ocr:
// it typesets project-authored text into a born-digital PDF, renders it with
// pdftoppm (poppler) and wraps the image into image-only PDFs, one per image
// encoding. Run from the repository root:
//
//	go run ./tools/ocrfixtures
package main

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// line is one typeset line: size in points, baseline y from the page top.
type line struct {
	text string
	size float64
	x, y float64
}

type fixture struct {
	name  string
	w, h  float64
	dpi   int
	lines []line
}

var fixtures = []fixture{{
	name: "field-report",
	w:    360, h: 270, dpi: 200,
	lines: []line{
		{"Quarterly Field Report", 18, 30, 44},
		{"The northern station recorded 42 clear days", 11, 30, 84},
		{"and 17 days of heavy rain this quarter.", 11, 30, 99},
		{"Soil samples were sent to the lab on May 3.", 11, 30, 129},
		{"- Replace the wind sensor", 11, 30, 164},
		{"- Calibrate the rain gauge", 11, 30, 179},
		{"Total cost: 1,250 USD", 11, 30, 214},
	},
}}

func main() {
	out := filepath.Join("testdata", "ocr")
	if err := os.MkdirAll(out, 0o755); err != nil {
		log.Fatal(err)
	}
	for _, fx := range fixtures {
		img, err := render(fx)
		if err != nil {
			log.Fatal(err)
		}
		var jpg bytes.Buffer
		if err := jpeg.Encode(&jpg, img, &jpeg.Options{Quality: 85}); err != nil {
			log.Fatal(err)
		}
		write(filepath.Join(out, fx.name+"-jpeg.pdf"), imagePDF(fx, img.Bounds(), "/DCTDecode", jpg.Bytes()))
		var flate bytes.Buffer
		zw, _ := zlib.NewWriterLevel(&flate, zlib.BestCompression)
		if _, err := zw.Write(img.Pix); err != nil {
			log.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			log.Fatal(err)
		}
		write(filepath.Join(out, fx.name+"-flate.pdf"), imagePDF(fx, img.Bounds(), "/FlateDecode", flate.Bytes()))
		var text []string
		for _, ln := range fx.lines {
			text = append(text, ln.text)
		}
		write(filepath.Join(out, fx.name+".txt"), []byte(strings.Join(text, "\n")+"\n"))
	}
}

func write(path string, data []byte) {
	if err := os.WriteFile(path, data, 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Println("wrote", path)
}

// render typesets the fixture in Helvetica and rasterizes it to 8-bit gray.
func render(fx fixture) (*image.Gray, error) {
	var content strings.Builder
	for _, ln := range fx.lines {
		fmt.Fprintf(&content, "BT /F1 %g Tf %g %g Td (%s) Tj ET\n", ln.size, ln.x, fx.h-ln.y, ln.text)
	}
	src := buildPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %g %g] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>", fx.w, fx.h),
		stream("", []byte(content.String())),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
	})
	dir, err := os.MkdirTemp("", "ocrfixtures")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	in := filepath.Join(dir, "in.pdf")
	if err := os.WriteFile(in, src, 0o644); err != nil {
		return nil, err
	}
	cmd := exec.Command("pdftoppm", "-r", fmt.Sprint(fx.dpi), "-gray", "-png", "-singlefile", in, filepath.Join(dir, "page"))
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("pdftoppm: %v: %s", err, out)
	}
	f, err := os.Open(filepath.Join(dir, "page.png"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	decoded, err := png.Decode(f)
	if err != nil {
		return nil, err
	}
	gray := image.NewGray(decoded.Bounds())
	for y := gray.Rect.Min.Y; y < gray.Rect.Max.Y; y++ {
		for x := gray.Rect.Min.X; x < gray.Rect.Max.X; x++ {
			gray.Set(x, y, decoded.At(x, y))
		}
	}
	return gray, nil
}

// imagePDF is a one-page PDF that paints one 8-bit gray image over the page.
func imagePDF(fx fixture, bounds image.Rectangle, filter string, data []byte) []byte {
	return buildPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %g %g] /Contents 4 0 R /Resources << /XObject << /Im1 5 0 R >> >> >>", fx.w, fx.h),
		stream("", fmt.Appendf(nil, "q %g 0 0 %g 0 0 cm /Im1 Do Q\n", fx.w, fx.h)),
		stream(fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter %s", bounds.Dx(), bounds.Dy(), filter), data),
	})
}

func stream(dict string, data []byte) string {
	return fmt.Sprintf("<< %s /Length %d >>\nstream\n%s\nendstream", dict, len(data), data)
}

func buildPDF(objects []string) []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
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
