package extract

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

const mergeFontSizeBand = 0.20

// mergeTextItems joins runs that share a baseline into single items: group
// by page and y (5pt band), sort by x, then walk runs together under
// style/gap rules with word-space insertion.
func mergeTextItems(items []TextItem) []TextItem {
	if len(items) == 0 {
		return items
	}
	type group struct {
		page  int
		y     float64
		items []*TextItem
	}
	var groups []*group
	for i := range items {
		item := &items[i]
		var found *group
		for _, g := range groups {
			if g.page == item.Page && math.Abs(item.Y-g.y) < 5.0 {
				found = g
				break
			}
		}
		if found == nil {
			found = &group{page: item.Page, y: item.Y}
			groups = append(groups, found)
		}
		found.items = append(found.items, item)
	}
	sort.SliceStable(groups, func(a, b int) bool {
		if groups[a].page != groups[b].page {
			return groups[a].page < groups[b].page
		}
		return groups[a].y > groups[b].y
	})

	var merged []TextItem
	for _, g := range groups {
		if g.items[0].Rotation != 0 {
			merged = append(merged, mergeSingleGroup(g.items)...)
			continue
		}
		if passes, ok := splitInterleavedPasses(g.items); ok {
			for _, pass := range passes {
				merged = append(merged, mergeSingleGroup(pass)...)
			}
			continue
		}
		sort.SliceStable(g.items, func(a, b int) bool { return g.items[a].X < g.items[b].X })
		merged = append(merged, mergeSingleGroup(g.items)...)
	}
	return merged
}

func mergeSingleGroup(items []*TextItem) []TextItem {
	var merged []TextItem
	i := 0
	for i < len(items) {
		first := items[i]
		text := first.Text
		endX := first.X + effectiveMergeWidth(first)
		boxRight := first.X + first.Width
		boxLeft := first.X
		runEnd, runFloor, tracked := trackedRunSpaceFloor(items, i)
		j := i + 1
		for j < len(items) {
			next := items[j]
			if math.Abs(next.FontSize-first.FontSize) > first.FontSize*mergeFontSizeBand {
				break
			}
			if next.IsItalic != first.IsItalic || next.IsUnderline != first.IsUnderline || next.IsStrikeout != first.IsStrikeout {
				break
			}
			if first.Rotation != 0 || next.Rotation != 0 {
				break
			}
			if next.AdvanceKnown != first.AdvanceKnown {
				break
			}
			gap := next.X - endX
			if gap > first.FontSize*0.5 {
				break
			}
			if gap < -first.FontSize*0.5 {
				break
			}
			threshold := first.FontSize * 0.08
			prevLast := lastRune(strings.TrimRight(text, " \t\n"))
			nextFirst := firstRune(strings.TrimLeft(next.Text, " \t\n"))
			switch {
			case isJoinPunct(nextFirst):
				threshold = first.FontSize * 0.25
			case isLower(prevLast) && isLower(nextFirst):
				threshold = first.FontSize * 0.13
			}
			if tracked && j <= runEnd {
				threshold = runFloor
			}
			boldBoundary := next.IsBold != first.IsBold
			explicitBoldSpace := boldBoundary &&
				(hasWhitespaceSuffix(text) || hasWhitespacePrefix(next.Text))
			numericBoundary := boldBoundary && numericJunction(text, next.Text)
			if gap > threshold && !numericBoundary && !hasWhitespaceSuffix(text) && !explicitBoldSpace {
				text += " "
			}
			if boldBoundary {
				break
			}
			text += next.Text
			boxRight = math.Max(boxRight, next.X+next.Width)
			boxLeft = math.Min(boxLeft, next.X)
			endX = next.X + effectiveMergeWidth(next)
			j++
		}
		out := *first
		out.Text = text
		if first.AdvanceKnown {
			out.X = first.X
			out.Width = endX - first.X
		} else {
			out.X = boxLeft
			out.Width = boxRight - boxLeft
		}
		merged = append(merged, out)
		i = j
	}
	return merged
}

// splitInterleavedPasses separates two strings painted glyph-by-glyph at the
// same baseline: an X sort weaves their glyphs together, but stream order
// still separates them into two monotonic X passes covering the same span.
func splitInterleavedPasses(items []*TextItem) ([][]*TextItem, bool) {
	if len(items) < 6 {
		return nil, false
	}
	for _, it := range items {
		if len([]rune(strings.TrimSpace(it.Text))) != 1 || containsCJK(it.Text) {
			return nil, false
		}
	}
	var passes [][]*TextItem
	pass := []*TextItem{items[0]}
	for _, it := range items[1:] {
		prev := pass[len(pass)-1]
		if it.X < prev.X-0.25*math.Abs(it.FontSize) {
			passes = append(passes, pass)
			pass = []*TextItem{it}
			continue
		}
		pass = append(pass, it)
	}
	passes = append(passes, pass)
	if len(passes) != 2 {
		return nil, false
	}
	aL, aR := passSpan(passes[0])
	bL, bR := passSpan(passes[1])
	overlap := math.Min(aR, bR) - math.Max(aL, bL)
	if overlap*2 < math.Min(aR-aL, bR-bL) {
		return nil, false
	}
	return passes, true
}

func passSpan(pass []*TextItem) (float64, float64) {
	l, r := pass[0].X, pass[0].X+pass[0].Width
	for _, it := range pass[1:] {
		l = math.Min(l, it.X)
		r = math.Max(r, it.X+it.Width)
	}
	return l, r
}

func effectiveMergeWidth(item *TextItem) float64 {
	if !item.AdvanceKnown {
		return item.Width
	}
	if item.Width <= 0 || item.FontSize <= 0 {
		return item.Width
	}
	if !strings.Contains(item.Text, " ") || containsCJK(item.Text) {
		return item.Width
	}
	charCount := len([]rune(item.Text))
	if charCount == 0 {
		return item.Width
	}
	avg := item.Width / float64(charCount)
	if avg > item.FontSize*0.85 {
		capped := float64(charCount) * item.FontSize * 0.6
		return math.Min(capped, item.Width)
	}
	return item.Width
}

func trackedRunSpaceFloor(group []*TextItem, start int) (int, float64, bool) {
	const minGaps = 4
	first := group[start]
	if first.Rotation != 0 || len([]rune(strings.TrimSpace(first.Text))) != 1 {
		return 0, 0, false
	}
	fs := first.FontSize
	if fs <= 0 {
		return 0, 0, false
	}
	var gaps []float64
	endX := first.X + effectiveMergeWidth(first)
	end := start
	for offset := start + 1; offset < len(group); offset++ {
		next := group[offset]
		if len([]rune(strings.TrimSpace(next.Text))) != 1 {
			break
		}
		if math.Abs(next.FontSize-fs) > fs*mergeFontSizeBand {
			break
		}
		if next.IsBold != first.IsBold || next.IsItalic != first.IsItalic ||
			next.IsUnderline != first.IsUnderline || next.IsStrikeout != first.IsStrikeout {
			break
		}
		if next.Rotation != 0 {
			break
		}
		if next.AdvanceKnown != first.AdvanceKnown {
			break
		}
		gap := next.X - endX
		if gap > fs*0.5 || gap < -fs*0.5 {
			break
		}
		gaps = append(gaps, gap/fs)
		endX = next.X + effectiveMergeWidth(next)
		end = offset
	}
	if len(gaps) < 2 {
		return 0, 0, false
	}
	sorted := append([]float64(nil), gaps...)
	sort.Float64s(sorted)
	median := sorted[len(sorted)/2]
	var hasCJK, allCJK, allCaps = false, true, true
	for _, it := range group[start : end+1] {
		for _, c := range strings.TrimSpace(it.Text) {
			if isSpacelessCJK(c) {
				hasCJK = true
			}
			if (unicode.IsLetter(c) || unicode.IsNumber(c)) && !isSpacelessCJK(c) {
				allCJK = false
			}
			if unicode.IsLetter(c) && !unicode.IsUpper(c) && !isCJKChar(c) {
				allCaps = false
			}
		}
	}
	spacelessCJK := hasCJK && allCJK
	if !spacelessCJK && !allCaps {
		return 0, 0, false
	}
	if len(gaps) >= minGaps {
		if median <= 0.075 {
			return 0, 0, false
		}
	} else {
		uniform := sorted[len(sorted)-1] <= math.Max(sorted[0], 0.01)*1.4
		if median < 0.09 || !uniform {
			return 0, 0, false
		}
	}
	if spacelessCJK {
		return end, math.Inf(1), true
	}
	bestJump := 1.0
	floor := math.Inf(1)
	for k := 0; k+1 < len(sorted); k++ {
		lo := math.Max(sorted[k], 0.01)
		hi := math.Max(sorted[k+1], 0.01)
		if jump := hi / lo; jump > bestJump {
			bestJump = jump
			floor = math.Sqrt(lo * hi)
		}
	}
	if bestJump < 1.4 {
		floor = math.Inf(1)
	}
	return end, floor * fs, true
}

func isSpacelessCJK(c rune) bool {
	switch {
	case c >= 0x3000 && c <= 0x303F,
		c >= 0x3040 && c <= 0x309F,
		c >= 0x30A0 && c <= 0x30FF,
		c >= 0x4E00 && c <= 0x9FFF,
		c >= 0xF900 && c <= 0xFAFF,
		c >= 0xFF00 && c <= 0xFFEF:
		return true
	}
	return false
}

func isCJKChar(c rune) bool {
	switch {
	case c >= 0x1100 && c <= 0x11FF,
		c >= 0x3000 && c <= 0x303F,
		c >= 0x3040 && c <= 0x309F,
		c >= 0x30A0 && c <= 0x30FF,
		c >= 0x3130 && c <= 0x318F,
		c >= 0x4E00 && c <= 0x9FFF,
		c >= 0xAC00 && c <= 0xD7AF,
		c >= 0xF900 && c <= 0xFAFF,
		c >= 0xFF00 && c <= 0xFFEF:
		return true
	}
	return false
}

func containsCJK(s string) bool {
	for _, c := range s {
		if c >= 0x4E00 && c <= 0x9FFF || c >= 0x3400 && c <= 0x4DBF || c >= 0xF900 && c <= 0xFAFF {
			return true
		}
	}
	return false
}

func lastRune(s string) rune {
	var last rune
	for _, c := range s {
		last = c
	}
	return last
}

func firstRune(s string) rune {
	for _, c := range s {
		return c
	}
	return 0
}

func isLower(c rune) bool {
	return unicode.IsLower(c)
}

func isJoinPunct(c rune) bool {
	switch c {
	case '.', ',', ';', ')', ']', '}':
		return true
	}
	return false
}

func numericJunction(text, next string) bool {
	p, pOK := lastRune(text), true
	if len(text) == 0 {
		pOK = false
	}
	c := firstRune(next)
	if !pOK {
		return false
	}
	if isDigitRune(p) && (isDigitRune(c) || c == '.' || c == ',' || c == '%') {
		return true
	}
	if (p == '.' || p == ',') && isDigitRune(c) {
		prefix := strings.TrimSuffix(text, string(p))
		trimmed := strings.TrimSpace(prefix)
		if trimmed == "" {
			return true
		}
		if isDigitRune(lastRune(trimmed)) {
			return true
		}
	}
	if (p == '+' || p == '-') && isDigitRune(c) {
		return true
	}
	return false
}

func isDigitRune(c rune) bool {
	return c >= '0' && c <= '9'
}

func hasWhitespaceSuffix(s string) bool {
	return strings.HasSuffix(s, " ") || strings.HasSuffix(s, "\t") || strings.HasSuffix(s, "\n")
}

func hasWhitespacePrefix(s string) bool {
	return strings.HasPrefix(s, " ") || strings.HasPrefix(s, "\t") || strings.HasPrefix(s, "\n")
}
