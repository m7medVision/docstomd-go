package extract

import (
	"math"
	"testing"
)

// capRun lays out s as one single-rune item per glyph at font size fs:
// letterGap and wordGap are the x gaps at letter and space junctions.
func capRun(s string, fs, letterGap, wordGap float64) []*TextItem {
	w := fs * 0.6
	x := 0.0
	var items []*TextItem
	for _, r := range s {
		if r == ' ' {
			x += wordGap
			continue
		}
		items = append(items, &TextItem{Text: string(r), X: x, Width: w, FontSize: fs, AdvanceKnown: true})
		x += w + letterGap
	}
	return items
}

func asValues(items []*TextItem) []TextItem {
	out := make([]TextItem, len(items))
	for i, it := range items {
		out[i] = *it
	}
	return out
}

func TestTrackedRunSpaceFloorBimodal(t *testing.T) {
	items := capRun("HOW NOW", 10, 1.0, 3.0)
	end, floor, ok := trackedRunSpaceFloor(items, 0)
	if !ok {
		t.Fatal("tracked run not detected")
	}
	if end != len(items)-1 {
		t.Fatalf("run end = %d, want %d", end, len(items)-1)
	}
	if floor <= 10*0.10 || floor >= 10*0.30 {
		t.Fatalf("floor = %v, want between letter (%v) and word (%v) gaps", floor, 10*0.10, 10*0.30)
	}
}

func TestTrackedRunSpaceFloorUniformGaps(t *testing.T) {
	items := capRun("TRACKED", 10, 1.0, 0)
	_, floor, ok := trackedRunSpaceFloor(items, 0)
	if !ok {
		t.Fatal("tracked run not detected")
	}
	if !math.IsInf(floor, 1) {
		t.Fatalf("floor = %v, want +Inf for unimodal gaps", floor)
	}
}

func TestTrackedRunSpaceFloorRefusesLowercase(t *testing.T) {
	items := capRun("abcde", 10, 3.0, 0)
	if _, _, ok := trackedRunSpaceFloor(items, 0); ok {
		t.Fatal("lowercase run must not be treated as tracked")
	}
}

func TestMergeTrackedCapsKeepsWords(t *testing.T) {
	merged := mergeTextItems(asValues(capRun("HOW NOW", 10, 1.0, 3.0)))
	if len(merged) != 1 {
		t.Fatalf("merged len = %d, want 1", len(merged))
	}
	if merged[0].Text != "HOW NOW" {
		t.Fatalf("text = %q, want %q", merged[0].Text, "HOW NOW")
	}
}

func TestMergeSpacedLowercaseKeepsBoundaries(t *testing.T) {
	merged := mergeTextItems(asValues(capRun("a b c", 10, 1.5, 0)))
	if len(merged) != 1 || merged[0].Text != "a b c" {
		t.Fatalf("text = %q, want %q", merged[0].Text, "a b c")
	}
}

// interleave paints a and b glyph-by-glyph at the same baseline, a first:
// two monotonic x passes sharing the line.
func interleave(a, b string, fs float64) []*TextItem {
	w := fs * 0.6
	step := w + 0.05*fs
	var items []*TextItem
	x := 0.0
	for _, r := range a {
		items = append(items, &TextItem{Text: string(r), X: x, Width: w, FontSize: fs, AdvanceKnown: true})
		x += step
	}
	x = 0.5 * fs
	for _, r := range b {
		items = append(items, &TextItem{Text: string(r), X: x, Width: w, FontSize: fs, AdvanceKnown: true})
		x += step
	}
	return items
}

func TestSplitInterleavedPassesSplitsOverlap(t *testing.T) {
	items := interleave("CONSOLID", "30/06/2", 10)
	passes, ok := splitInterleavedPasses(items)
	if !ok {
		t.Fatal("interleaved passes not split")
	}
	if len(passes) != 2 {
		t.Fatalf("passes = %d, want 2", len(passes))
	}
	wantA, wantB := "CONSOLID", "30/06/2"
	if got := textOfPass(passes[0]); got != wantA {
		t.Fatalf("pass 0 = %q, want %q", got, wantA)
	}
	if got := textOfPass(passes[1]); got != wantB {
		t.Fatalf("pass 1 = %q, want %q", got, wantB)
	}
}

func TestSplitInterleavedPassesKeepsMonotonicLine(t *testing.T) {
	items := interleave("CONSOLID", "", 10)
	if _, ok := splitInterleavedPasses(items); ok {
		t.Fatal("single monotonic line must not split")
	}
}

func TestSplitInterleavedPassesRefusesSingleItem(t *testing.T) {
	items := []*TextItem{{Text: "A", X: 0, Width: 6, FontSize: 10, AdvanceKnown: true}}
	if _, ok := splitInterleavedPasses(items); ok {
		t.Fatal("single item must not split")
	}
}

func TestMergeSeparatesInterleavedPasses(t *testing.T) {
	merged := mergeTextItems(asValues(interleave("CONSOLID", "30/06/2", 10)))
	if len(merged) != 2 {
		t.Fatalf("merged len = %d, want 2", len(merged))
	}
	if merged[0].Text != "CONSOLID" || merged[1].Text != "30/06/2" {
		t.Fatalf("texts = %q, %q", merged[0].Text, merged[1].Text)
	}
}

func textOfPass(pass []*TextItem) string {
	s := ""
	for _, it := range pass {
		s += it.Text
	}
	return s
}

func TestExpandLigaturesReversesVisualArabic(t *testing.T) {
	if got := expandLigatures("رﺎﻔظ"); got != "ظفار" {
		t.Fatalf("got %q, want %q", got, "ظفار")
	}
}

func TestExpandLigaturesKeepsDigitsForward(t *testing.T) {
	if got := expandLigatures("2026 رﺎﻔظ"); got != "ظفار 2026" {
		t.Fatalf("got %q, want %q", got, "ظفار 2026")
	}
}

func TestExpandLigaturesLeavesLatinAlone(t *testing.T) {
	if got := expandLigatures("Hello 2026"); got != "Hello 2026" {
		t.Fatalf("got %q", got)
	}
	if got := expandLigatures("ﬁne"); got != "fine" {
		t.Fatalf("ligature fallback: got %q", got)
	}
}
