package engine_test

import (
	"testing"

	"github.com/m7medVision/docstomd-go/ocr/backend"
	"github.com/m7medVision/docstomd-go/ocr/engine"
	"github.com/m7medVision/docstomd-go/ocr/internal/ocrtest"
	"github.com/m7medVision/docstomd-go/ocr/pageimage"
)

func load(t *testing.T, id string) *engine.Engine {
	t.Helper()
	dir := ocrtest.ModelDir(t, id)
	sel, err := backend.New("go")
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
