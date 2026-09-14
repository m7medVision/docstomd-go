package extract

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

const (
	scriptMaxRatio        = 0.75
	scriptMinRatio        = 0.4
	scriptMinShift        = 0.1
	scriptMaxRaise        = 0.6
	scriptMaxDrop         = 0.45
	scriptAttachOverlap   = 0.5
	scriptAttachGap       = 0.25
	scriptChainGap        = 0.35
	scriptRunBaselineTol  = 0.75
	scriptMaxRunChars     = 12
	scriptMaxGlyphChars   = 8
	scriptMaxGlyphLetters = 4
	scriptMaxFusedDigits  = 4
)

type scriptRun struct {
	glyphs       []int
	anchor       int
	anchorOnLeft bool
}

func isScriptMarkerSymbol(c rune) bool {
	switch c {
	case '*', '†', '‡', '§', '¶', '‖', '#', '∗', '⁎', '⋆', '®', '™', '©',
		',', ';', ':', '.', '(', ')', '[', ']', '{', '}', '+', '-', '−', '–', '/', '\'', '′', '″':
		return true
	}
	return false
}

func isScriptGlyphText(text string) bool {
	if strings.TrimSpace(text) != text || text == "" {
		return false
	}
	chars := 0
	letters := 0
	for _, c := range text {
		chars++
		if unicode.IsSpace(c) {
			return false
		}
		if unicode.IsLetter(c) {
			letters++
		} else if !(unicode.IsDigit(c) || isScriptMarkerSymbol(c)) {
			return false
		}
	}
	return chars > 0 && chars <= scriptMaxGlyphChars && letters <= scriptMaxGlyphLetters
}

func itemRight(item *TextItem) float64 { return item.X + effectiveMergeWidth(item) }

func scriptAnchorGap(first, last *TextItem, runFS float64, body *TextItem) (float64, bool, bool) {
	ratio := runFS / body.FontSize
	if ratio < scriptMinRatio || ratio > scriptMaxRatio {
		return 0, false, false
	}
	dy := first.Y - body.Y
	if math.Abs(dy) < body.FontSize*scriptMinShift ||
		dy > body.FontSize*scriptMaxRaise ||
		dy < -body.FontSize*scriptMaxDrop {
		return 0, false, false
	}
	lo := -body.FontSize * scriptAttachOverlap
	hi := body.FontSize * scriptAttachGap
	bodyCenter := (body.X + itemRight(body)) / 2
	runCenter := (first.X + itemRight(last)) / 2
	if bodyCenter <= runCenter {
		gap := first.X - itemRight(body)
		edgeOK := lastRune(body.Text) != 0 && !unicode.IsSpace(lastRune(body.Text))
		if gap >= lo && gap <= hi && edgeOK {
			return math.Max(gap, 0), true, true
		}
		return 0, false, false
	}
	gap := body.X - itemRight(last)
	edgeOK := firstRune(body.Text) != 0 && !unicode.IsSpace(firstRune(body.Text))
	if gap >= lo && gap <= hi && edgeOK {
		return math.Max(gap, 0), true, false
	}
	return 0, false, false
}

// mergeSubscriptItems orders items into (page, y-window, x) groups, chains
// glyph-shaped small runs, and attaches each to the nearest larger anchor —
// fusing digit-only runs as Unicode super/subscript characters,
// materializing others as shifted runs.
func mergeSubscriptItems(items []TextItem) []TextItem {
	if len(items) < 2 {
		return items
	}
	byPage := map[int][]int{}
	for i := range items {
		if items[i].ItemType == ItemText && items[i].FontSize > 0 && strings.TrimSpace(items[i].Text) != "" {
			byPage[items[i].Page] = append(byPage[items[i].Page], i)
		}
	}
	pages := make([]int, 0, len(byPage))
	for p := range byPage {
		pages = append(pages, p)
	}
	sort.Ints(pages)

	var runs []scriptRun
	for _, page := range pages {
		byY := byPage[page]
		sort.SliceStable(byY, func(a, b int) bool {
			return items[byY[a]].Y < items[byY[b]].Y
		})
		maxFS := 0.0
		for _, i := range byY {
			maxFS = math.Max(maxFS, items[i].FontSize)
		}
		window := maxFS * math.Max(scriptMaxRaise, scriptMaxDrop)

		var glyphs []int
		for _, i := range byY {
			if isScriptGlyphText(items[i].Text) {
				glyphs = append(glyphs, i)
			}
		}
		sort.SliceStable(glyphs, func(a, b int) bool {
			ga, gb := glyphs[a], glyphs[b]
			if items[ga].X != items[gb].X {
				return items[ga].X < items[gb].X
			}
			return items[ga].Y < items[gb].Y
		})
		var chains [][]int
		for _, g := range glyphs {
			glyph := &items[g]
			joined := -1
			for c := len(chains) - 1; c >= 0; c-- {
				last := &items[chains[c][len(chains[c])-1]]
				fs := math.Max(last.FontSize, glyph.FontSize)
				gap := glyph.X - itemRight(last)
				if math.Abs(last.Y-glyph.Y) <= scriptRunBaselineTol &&
					math.Abs(last.FontSize-glyph.FontSize) <= fs*0.2 &&
					gap <= fs*scriptChainGap && gap >= -fs {
					joined = c
					break
				}
			}
			if joined >= 0 {
				chains[joined] = append(chains[joined], g)
			} else {
				chains = append(chains, []int{g})
			}
		}

		for _, chain := range chains {
			chars := 0
			for _, g := range chain {
				chars += len([]rune(items[g].Text))
			}
			if chars > scriptMaxRunChars {
				continue
			}
			first := &items[chain[0]]
			last := &items[chain[len(chain)-1]]
			runFS := 0.0
			for _, g := range chain {
				runFS = math.Max(runFS, items[g].FontSize)
			}
			bestGap := math.Inf(1)
			bestAnchor, bestOnLeft := -1, false
			for _, n := range byY {
				body := &items[n]
				if body.Y > first.Y+window || body.Y < first.Y-window {
					continue
				}
				inChain := false
				for _, g := range chain {
					if g == n {
						inChain = true
						break
					}
				}
				if inChain {
					continue
				}
				gap, ok, onLeft := scriptAnchorGap(first, last, runFS, body)
				if !ok {
					continue
				}
				better := gap < bestGap-0.01 || (math.Abs(gap-bestGap) <= 0.01 && onLeft && !bestOnLeft)
				if better {
					bestGap, bestAnchor, bestOnLeft = gap, n, onLeft
				}
			}
			if bestAnchor >= 0 {
				runs = append(runs, scriptRun{glyphs: chain, anchor: bestAnchor, anchorOnLeft: bestOnLeft})
			}
		}
	}

	runOfGlyph := map[int]int{}
	for ri, run := range runs {
		for _, g := range run.glyphs {
			runOfGlyph[g] = ri
		}
	}
	bodyAnchorY := make([]float64, len(runs))
	for ri, run := range runs {
		idx := run.anchor
		for i := 0; i < 8; i++ {
			if r, ok := runOfGlyph[idx]; ok {
				idx = runs[r].anchor
			} else {
				break
			}
		}
		bodyAnchorY[ri] = items[idx].Y
	}

	remove := make([]bool, len(items))
	for ri, run := range runs {
		anchor := &items[run.anchor]
		digits := 0
		for _, g := range run.glyphs {
			digits += len([]rune(items[g].Text))
		}
		digitOnly := true
		for _, g := range run.glyphs {
			for _, c := range items[g].Text {
				if c < '0' || c > '9' {
					digitOnly = false
				}
			}
		}
		marksOK := true
		for _, g := range run.glyphs {
			glyph := &items[g]
			if anchor.IsStrikeout != glyph.IsStrikeout {
				marksOK = false
			}
			if anchor.IsUnderline != glyph.IsUnderline && !(anchor.IsUnderline && !glyph.IsUnderline) {
				marksOK = false
			}
		}
		fuse := digitOnly && digits <= scriptMaxFusedDigits && marksOK &&
			fusableAnchorEdge(anchor, run.anchorOnLeft) &&
			runOfGlyph[run.anchor] == 0 && !glyphInRuns(run.anchor, runs, ri)
		if fuse {
			raised := items[run.glyphs[0]].Y > anchor.Y+anchor.FontSize*0.1
			mapped := ""
			for _, g := range run.glyphs {
				mapped += mapScriptDigits(items[g].Text, raised)
			}
			runLeft := items[run.glyphs[0]].X
			runRight := itemRight(&items[run.glyphs[len(run.glyphs)-1]])
			anchorRight := itemRight(anchor)
			if run.anchorOnLeft {
				anchor.Text += mapped
				anchor.Width = math.Max(anchorRight, runRight) - anchor.X
			} else {
				anchor.Text = mapped + anchor.Text
				left := math.Min(anchor.X, runLeft)
				anchor.Width = math.Max(anchorRight, runRight) - left
				anchor.X = left
			}
			for _, g := range run.glyphs {
				remove[g] = true
			}
		} else {
			anchorY := bodyAnchorY[ri]
			runRight := itemRight(&items[run.glyphs[len(run.glyphs)-1]])
			text := ""
			for _, g := range run.glyphs {
				text += items[g].Text
			}
			head := &items[run.glyphs[0]]
			head.Text = text
			head.Width = runRight - head.X
			head.BaselineShift = head.Y - anchorY
			for _, g := range run.glyphs[1:] {
				remove[g] = true
			}
		}
	}

	out := items[:0]
	for i, item := range items {
		if !remove[i] {
			out = append(out, item)
		}
	}
	return out
}

func glyphInRuns(idx int, runs []scriptRun, except int) bool {
	for ri, run := range runs {
		if ri == except {
			continue
		}
		for _, g := range run.glyphs {
			if g == idx {
				return true
			}
		}
	}
	return false
}

func fusableAnchorEdge(anchor *TextItem, anchorOnLeft bool) bool {
	if anchorOnLeft {
		c := lastRune(anchor.Text)
		return unicode.IsLetter(c) || isFusablePunct(c)
	}
	c := firstRune(anchor.Text)
	return unicode.IsLetter(c)
}

func isFusablePunct(c rune) bool {
	switch c {
	case '.', ',', ';', ':', '!', '?', ')', ']', '}', '"', '\'', '”', '’':
		return true
	}
	return false
}

func mapScriptDigits(text string, raised bool) string {
	sup := map[rune]rune{'0': '⁰', '1': '¹', '2': '²', '3': '³', '4': '⁴', '5': '⁵', '6': '⁶', '7': '⁷', '8': '⁸', '9': '⁹'}
	sub := map[rune]rune{'0': '₀', '1': '₁', '2': '₂', '3': '₃', '4': '₄', '5': '₅', '6': '₆', '7': '₇', '8': '₈', '9': '₉'}
	m := sub
	if raised {
		m = sup
	}
	var sb strings.Builder
	for _, c := range text {
		if r, ok := m[c]; ok {
			sb.WriteRune(r)
		} else {
			sb.WriteRune(c)
		}
	}
	return sb.String()
}
