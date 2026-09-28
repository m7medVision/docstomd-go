package ocr

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/m7medVision/docstomd-go/internal/pdf/markdown"
)

// pixelLine places a line on a 1275×1650 page image (US Letter at 150 dpi).
func pixelLine(text string, x, top, height float64) Line {
	return Line{Text: text, Box: Rect{X0: x, Y0: top, X1: x + float64(len(text))*height*0.45, Y1: top + height}, Confidence: 0.95}
}

func letterPage(lines ...Line) PageResult {
	return PageResult{Page: 1, Width: 1275, Height: 1650, Lines: lines}
}

func TestLinesMarkdownParagraphsHeadingsLists(t *testing.T) {
	page := letterPage(
		pixelLine("Annual Report", 150, 150, 60),
		pixelLine("The first paragraph starts here and", 150, 260, 30),
		pixelLine("continues on a second line.", 150, 295, 30),
		pixelLine("A second paragraph after a gap.", 150, 420, 30),
		pixelLine("• apples", 150, 520, 30),
		pixelLine("• pears", 150, 555, 30),
		pixelLine("1. first step", 150, 640, 30),
	)
	got := LinesMarkdown(page, 612, 792, markdown.DefaultOptions())
	want := "# Annual Report\n\nThe first paragraph starts here and continues on a second line.\n\nA second paragraph after a gap.\n\n- apples\n\n- pears\n\n1. first step"
	if got != want {
		t.Errorf("markdown:\n%s\nwant:\n%s", got, want)
	}
}

func TestLinesBoxJitterDoesNotInventHeadingLevels(t *testing.T) {
	var lines []Line
	lines = append(lines, pixelLine("Main Title", 150, 100, 64))
	top := 220.0
	for i := range 12 {
		// ±8% height jitter, typical of a text detector.
		h := 30 * (1 + 0.08*float64(i%3-1))
		lines = append(lines, pixelLine(fmt.Sprintf("Body line %d with ordinary words in it.", i), 150, top, h))
		top += 36
	}
	got := LinesMarkdown(letterPage(lines...), 612, 792, markdown.DefaultOptions())
	if strings.Count(got, "#") != 1 || !strings.HasPrefix(got, "# Main Title\n") {
		t.Errorf("want one heading and body text, got:\n%s", got)
	}
}

func TestLineItemsScaleAndFlip(t *testing.T) {
	items := LineItems(PageResult{Page: 3, Width: 1275, Height: 1650, Lines: []Line{{Text: " hello   world ", Box: Rect{X0: 150, Y0: 300, X1: 450, Y1: 330}}}}, 612, 792)
	if len(items) != 1 {
		t.Fatalf("items = %d", len(items))
	}
	it := items[0]
	if it.Text != "hello world" || it.Page != 3 {
		t.Errorf("item = %+v", it)
	}
	if it.X != 72 || it.Y != 792-330*0.48 || it.Width != 144 {
		t.Errorf("geometry x=%v y=%v w=%v", it.X, it.Y, it.Width)
	}
	if it.FontSize != 12 {
		t.Errorf("font size = %v, want 12 (14.4pt box / 1.2)", it.FontSize)
	}
}

func TestLineItemsSkipEmptyAndDegenerate(t *testing.T) {
	items := LineItems(PageResult{Lines: []Line{
		{Text: "  ", Box: Rect{0, 0, 10, 10}},
		{Text: "flat", Box: Rect{0, 5, 10, 5}},
		{Text: "ok", Box: Rect{0, 0, 10, 12}},
	}}, 0, 0)
	if len(items) != 1 || items[0].Text != "ok" {
		t.Errorf("items = %+v", items)
	}
}

type linesProvider struct{ pages map[int]PageResult }

func (p *linesProvider) Name() string         { return "lines" }
func (p *linesProvider) EstPageCost() float64 { return 0 }
func (p *linesProvider) Recognize(_ context.Context, _ Document, pages []int) ([]PageResult, error) {
	var out []PageResult
	for _, n := range pages {
		if r, ok := p.pages[n]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

func TestRouterRendersLinesAndChecksQuality(t *testing.T) {
	good := letterPage(pixelLine("Invoice total is due within thirty days.", 150, 200, 30))
	weak := letterPage(pixelLine("low confidence words here", 150, 200, 30))
	weak.Page = 2
	weak.Lines[0].Confidence = 0.2
	empty := PageResult{Page: 3, Width: 100, Height: 100}
	provider := &linesProvider{pages: map[int]PageResult{1: good, 2: weak, 3: empty}}
	r := &Router{}
	result, err := r.Run(context.Background(), provider, Document{}, Force, nil, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.PageMarkdown[1] != "Invoice total is due within thirty days." {
		t.Errorf("page 1 = %q", result.PageMarkdown[1])
	}
	if fmt.Sprint(result.NeedsReview) != "[2 3]" {
		t.Errorf("needs review = %v, want [2 3]", result.NeedsReview)
	}
}

func TestRouterPrefersMarkdownOverLines(t *testing.T) {
	page := letterPage(pixelLine("from lines", 150, 200, 30))
	page.Markdown = "from markdown"
	page.Confidence = 0.9
	r := &Router{Lines: func(PageResult) string { t.Error("lines rendered despite Markdown"); return "" }}
	result, err := r.Run(context.Background(), &linesProvider{pages: map[int]PageResult{1: page}}, Document{}, Force, nil, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.PageMarkdown[1] != "from markdown" {
		t.Errorf("page 1 = %q", result.PageMarkdown[1])
	}
}
