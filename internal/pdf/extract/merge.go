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
	for _, g := range groups {
		if g.items[0].Rotation != 0 {
			continue
		}
		sort.SliceStable(g.items, func(a, b int) bool { return g.items[a].X < g.items[b].X })
	}
	sort.SliceStable(groups, func(a, b int) bool {
		if groups[a].page != groups[b].page {
			return groups[a].page < groups[b].page
		}
		return groups[a].y > groups[b].y
	})

	var merged []TextItem
	for _, g := range groups {
		i := 0
		for i < len(g.items) {
			first := g.items[i]
			text := first.Text
			endX := first.X + effectiveMergeWidth(first)
			boxRight := first.X + first.Width
			boxLeft := first.X
			j := i + 1
			for j < len(g.items) {
				next := g.items[j]
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
	}
	return merged
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
