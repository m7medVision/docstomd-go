package extract

import (
	"math"
	"strings"
	"testing"

	"github.com/m7medVision/docstomd-go/internal/pdf/parse"
)

func extractContent(t *testing.T, content string) []TextItem {
	t.Helper()
	doc, err := parse.Parse(buildContentPDF([]byte(content)))
	if err != nil {
		t.Fatalf("parse wrapper PDF: %v", err)
	}
	pages := Extract(doc)
	if len(pages) != 1 {
		t.Fatalf("pages = %d, want 1", len(pages))
	}
	return pages[0].Items
}

func TestShortQuoteOperatorKeepsPageText(t *testing.T) {
	items := extractContent(t, `BT /F1 12 Tf 72 700 Td (abc) " (keep) Tj ET`)
	var texts []string
	for _, item := range items {
		texts = append(texts, item.Text)
	}
	if got := strings.Join(texts, " "); got != "keep" {
		t.Errorf("text = %q, want %q", got, "keep")
	}
}

func TestQuoteOperatorSetsSpacingAndMovesLine(t *testing.T) {
	items := extractContent(t, `BT /F1 12 Tf 14 TL 72 700 Td (first) Tj 0 0 (second) " ET`)
	if len(items) != 2 {
		t.Fatalf("items = %d (%v), want 2", len(items), items)
	}
	if items[0].Text != "first" || items[1].Text != "second" {
		t.Fatalf("texts = %q, %q, want first, second", items[0].Text, items[1].Text)
	}
	if dy := math.Abs(items[0].Y - items[1].Y); math.Abs(dy-14) > 0.01 {
		t.Errorf(`" moved the line by %v, want the leading 14`, dy)
	}
}

// TestOperatorsWithTooFewOperandsDoNotPanic runs every operator the
// interpreter handles with each operand count from none to one short of the
// most any operator takes.
func TestOperatorsWithTooFewOperandsDoNotPanic(t *testing.T) {
	operators := []string{
		"q", "Q", "cm", "BT", "ET", "Tf", "Tc", "Tw", "Tz", "TL", "Ts", "Tr",
		"Td", "TD", "Tm", "T*", "Tj", "'", `"`, "TJ", "re", "m", "l", "S", "s",
		"f", "F", "f*", "B", "B*", "b", "b*", "n", "BDC", "BMC", "EMC", "Do",
	}
	for _, op := range operators {
		for n := 0; n < 6; n++ {
			content := "BT /F1 12 Tf 72 700 Td " + strings.Repeat("(x) ", n) + op + " ET"
			t.Run(content, func(t *testing.T) {
				extractContent(t, content)
			})
		}
	}
}

func TestDetectUnderlinesRawIgnoresTableRulesInAnyOrder(t *testing.T) {
	tableRule := func(y float64) Rect { return Rect{X: 0, Y: y, Width: 200, Height: 0.5, Page: 1} }
	underline := Rect{X: 300, Y: 499, Width: 50, Height: 0.5, Page: 1}
	// A table rule, a table rule, the underline, then the last table rule:
	// the filter must still see all three table rules when it checks the
	// last one.
	painted := []Rect{tableRule(100), tableRule(80), underline, tableRule(60)}
	items := []TextItem{
		{Text: "underlined", Page: 1, X: 300, Y: 500, Width: 50, Height: 10, FontSize: 10, ItemType: ItemText},
		{Text: "cell", Page: 1, X: 10, Y: 61, Width: 50, Height: 10, FontSize: 10, ItemType: ItemText},
	}
	detectUnderlinesRaw(items, painted, nil)
	if !items[0].IsUnderline {
		t.Error("text over the real underline is not marked underlined")
	}
	if items[1].IsUnderline {
		t.Error("text over the last table rule is marked underlined")
	}
}

func TestDecodeFallbackChar(t *testing.T) {
	tests := []struct {
		code byte
		want rune
	}{
		{0x20, ' '},
		{0x41, 'A'},
		{0x7E, '~'},
		{0x80, '€'},
		{0x85, '…'},
		{0x93, '“'},
		{0x9F, 'Ÿ'},
		{0x81, 0x81}, // undefined in cp1252
		{0xA0, 0xA0},
		{0xC1, 'Á'},
		{0xE9, 'é'},
		{0xFF, 'ÿ'},
	}
	for _, tt := range tests {
		if got := decodeFallbackChar(tt.code); got != tt.want {
			t.Errorf("decodeFallbackChar(%#x) = %q, want %q", tt.code, got, tt.want)
		}
	}
}
