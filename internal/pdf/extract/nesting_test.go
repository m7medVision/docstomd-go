package extract

import (
	"strconv"
	"strings"
	"testing"

	"github.com/m7medVision/docstomd-go/internal/pdf/parse"
	"github.com/m7medVision/docstomd-go/internal/pdftest"
)

func TestFormXObjectChainStopsAtDepthCap(t *testing.T) {
	doc, err := parse.Parse(pdftest.FormChain(maxFormDepth + 8))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	pages := Extract(doc)
	if len(pages) != 1 {
		t.Fatalf("got %d pages, want 1", len(pages))
	}
	drawn := map[string]bool{}
	for _, item := range pages[0].Items {
		drawn[strings.TrimSpace(item.Text)] = true
	}
	for i := 1; i <= maxFormDepth+8; i++ {
		name := "form" + strconv.Itoa(i)
		if want := i <= maxFormDepth; drawn[name] != want {
			t.Errorf("%s drawn = %v, want %v", name, drawn[name], want)
		}
	}
}

func TestGraphicsStateStackCap(t *testing.T) {
	const pushes = maxGStackDepth + 50
	it := newInterp(nil, 1, nil, map[string]*fontContext{})
	it.run([]byte(strings.Repeat("q ", pushes) + "2 0 0 2 5 5 cm"))
	if got := len(it.gstack); got != maxGStackDepth {
		t.Errorf("stack depth = %d, want %d", got, maxGStackDepth)
	}
	if got, want := it.ctm, (mat{2, 0, 0, 2, 5, 5}); got != want {
		t.Errorf("ctm inside = %v, want %v", got, want)
	}
	// The first 50 Qs match the unsaved pushes and keep the state; the rest
	// unwind the stack back to the identity matrix.
	it.run([]byte(strings.Repeat("Q ", 50)))
	if got, want := it.ctm, (mat{2, 0, 0, 2, 5, 5}); got != want {
		t.Errorf("ctm after overflow Qs = %v, want %v", got, want)
	}
	it.run([]byte(strings.Repeat("Q ", maxGStackDepth)))
	if got, want := it.ctm, (mat{1, 0, 0, 1, 0, 0}); got != want {
		t.Errorf("ctm after all Qs = %v, want %v", got, want)
	}
	if len(it.gstack) != 0 || it.gstackOverflow != 0 {
		t.Errorf("stack = %d, overflow = %d; want both 0", len(it.gstack), it.gstackOverflow)
	}
}

func TestDeepQNestingStillExtractsText(t *testing.T) {
	doc, err := parse.Parse(pdftest.QNesting(100_000))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	pages := Extract(doc)
	if len(pages) != 1 || len(pages[0].Items) == 0 || !strings.Contains(pages[0].Items[0].Text, "deep") {
		t.Errorf("pages = %+v, want the text \"deep\"", pages)
	}
}

func TestDeeplyNestedOperandsAreSkipped(t *testing.T) {
	content := strings.Repeat("[", 200_000) + " BT /F1 12 Tf 72 700 Td (after) Tj ET"
	doc, err := parse.Parse(buildContentPDF([]byte(content)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	pages := Extract(doc)
	var texts []string
	for _, item := range pages[0].Items {
		texts = append(texts, item.Text)
	}
	if !strings.Contains(strings.Join(texts, " "), "after") {
		t.Errorf("texts = %q, want the text after the nesting", texts)
	}
}
