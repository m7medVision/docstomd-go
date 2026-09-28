package engine_test

import (
	"image"
	"strings"
	"testing"

	"github.com/m7medVision/docstomd-go/ocr/backend"
	"github.com/m7medVision/docstomd-go/ocr/engine"
	"github.com/m7medVision/docstomd-go/ocr/internal/ocrtest"
	"github.com/m7medVision/docstomd-go/ocr/pageimage"
)

func load(t *testing.T, id string) *engine.Engine {
	t.Helper()
	dir := ocrtest.ModelDir(t, id)
	sel, err := backend.New(ocrtest.Backend())
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.Load(dir, sel.Backend)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Close)
	return eng
}

func recognize(t *testing.T, eng *engine.Engine, fixture string) []ocrtest.GoldenLine {
	t.Helper()
	doc, err := pageimage.Open(ocrtest.Fixture(t, fixture))
	if err != nil {
		t.Fatal(err)
	}
	page, err := doc.Extract(1)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := eng.Recognize(page.Image)
	if err != nil {
		t.Fatal(err)
	}
	var got []ocrtest.GoldenLine
	for _, l := range lines {
		got = append(got, ocrtest.GoldenLine{Text: l.Text, Box: [4]float64{l.X0, l.Y0, l.X1, l.Y1}})
	}
	return got
}

func TestPPOCRv5MobileGoldens(t *testing.T) {
	eng := load(t, "pp-ocrv5-mobile")
	want := ocrtest.Golden(t, "field-report")
	for _, fixture := range []string{"field-report-jpeg.pdf", "field-report-flate.pdf"} {
		t.Run(fixture, func(t *testing.T) {
			ocrtest.CompareLines(t, recognize(t, eng, fixture), want)
		})
	}
}

func TestNoDenormalWeightsAfterLoad(t *testing.T) {
	eng := load(t, "pp-ocrv5-mobile")
	if eng.Flushed() == 0 {
		t.Log("model carried no denormal weights")
	}
	if n := eng.Denormals(); n != 0 {
		t.Errorf("%d denormal weights left after load", n)
	}
}

func pageImage(t *testing.T, fixture string) image.Image {
	t.Helper()
	doc, err := pageimage.Open(ocrtest.Fixture(t, fixture))
	if err != nil {
		t.Fatal(err)
	}
	page, err := doc.Extract(1)
	if err != nil {
		t.Fatal(err)
	}
	return page.Image
}

// scaled resizes img by f (nearest neighbour is enough for a size change).
func scaled(img image.Image, f float64) image.Image {
	b := img.Bounds()
	w, h := int(float64(b.Dx())*f), int(float64(b.Dy())*f)
	out := image.NewGray(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			out.Set(x, y, img.At(b.Min.X+int(float64(x)/f), b.Min.Y+int(float64(y)/f)))
		}
	}
	return out
}

func texts(t *testing.T, eng *engine.Engine, img image.Image) []string {
	t.Helper()
	lines, err := eng.Recognize(img)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range lines {
		out = append(out, strings.Join(strings.Fields(l.Text), ""))
	}
	return out
}

func TestBucketsCompileOncePerSession(t *testing.T) {
	eng := load(t, "pp-ocrv5-mobile")
	m := eng.Manifest
	if len(m.Detector.Buckets) == 0 || len(m.Recognizer.Widths) == 0 || len(m.Recognizer.Batches) == 0 {
		t.Fatal("manifest declares no buckets")
	}
	first := pageImage(t, "field-report-jpeg.pdf")
	pages := []image.Image{first, pageImage(t, "field-report-flate.pdf"), scaled(first, 0.9), scaled(first, 0.75)}
	for _, img := range pages {
		texts(t, eng, img)
	}
	// Every compiled shape is a declared bucket: detector sizes, recognizer
	// widths × batches, classifier batches.
	limit := int64(len(m.Detector.Buckets) + len(m.Recognizer.Widths)*len(m.Recognizer.Batches) + len(m.Classifier.Batches))
	builds := eng.Builds()
	if builds > limit {
		t.Errorf("%d graph builds for %d pages, more than the %d declared buckets", builds, len(pages), limit)
	}
	// A warm session recompiles nothing.
	for _, img := range pages {
		texts(t, eng, img)
	}
	if got := eng.Builds(); got != builds {
		t.Errorf("graph builds grew from %d to %d on pages whose buckets were compiled", builds, got)
	}
}

func TestPaddingKeepsText(t *testing.T) {
	eng := load(t, "pp-ocrv5-mobile")
	img := scaled(pageImage(t, "field-report-jpeg.pdf"), 0.9)
	padded := texts(t, eng, img)
	eng.Manifest.Detector.Buckets = nil
	eng.Manifest.Recognizer.Widths = nil
	eng.Manifest.Recognizer.Batches = nil
	exact := texts(t, eng, img)
	if strings.Join(padded, "|") != strings.Join(exact, "|") {
		t.Errorf("padded text %q differs from exact-shape text %q", padded, exact)
	}
	want := ocrtest.Golden(t, "field-report")
	if len(exact) != len(want) {
		t.Errorf("exact-shape run found %d lines, want %d: %q", len(exact), len(want), exact)
	}
}

// TestArabicLogicalOrder: the Arabic recognizer reads right-to-left lines
// in visual order; the engine returns them in logical order with digits and
// Latin words intact.
func TestArabicLogicalOrder(t *testing.T) {
	eng := load(t, "pp-ocrv5-arabic-mobile")
	if eng.Manifest.OutputOrder != "visual" {
		t.Fatalf("output_order = %q, want visual", eng.Manifest.OutputOrder)
	}
	ocrtest.CompareLines(t, recognize(t, eng, "arabic-mixed.pdf"), ocrtest.Golden(t, "arabic-mixed"))
}

// TestLogicalModelsUntouched: models declaring logical order are not
// reordered.
func TestLogicalModelsUntouched(t *testing.T) {
	eng := load(t, "pp-ocrv5-mobile")
	if eng.Manifest.OutputOrder == "visual" {
		t.Fatal("pp-ocrv5-mobile declares visual order")
	}
	ocrtest.CompareLines(t, recognize(t, eng, "field-report-flate.pdf"), ocrtest.Golden(t, "field-report"))
}
