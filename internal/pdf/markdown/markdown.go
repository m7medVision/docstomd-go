// Package markdown turns extracted items into GitHub-Flavored Markdown.
package markdown

import (
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/m7medVision/docstomd-go/internal/pdf/extract"
	"github.com/m7medVision/docstomd-go/internal/pdf/table"
)

type Options struct {
	DetectHeadings bool
	DetectLists    bool
	DetectCode     bool
	RemovePageNums bool
	FixHyphenation bool
	StripFurniture bool
	IncludeLinks   bool
}

func DefaultOptions() Options {
	return Options{
		DetectHeadings: true,
		DetectLists:    true,
		DetectCode:     true,
		RemovePageNums: true,
		FixHyphenation: true,
		StripFurniture: true,
	}
}

func pageNumOf(page extract.PageResult, index int) int {
	if len(page.Items) > 0 {
		return page.Items[0].Page
	}
	return index + 1
}

type Complexity struct {
	IsComplex       bool  `json:"is_complex"`
	PagesWithCols   []int `json:"pages_with_columns"`
	PagesWithTables []int `json:"pages_with_tables"`
}

type line struct {
	page  int
	items []extract.TextItem
	y     float64
	x     float64
}

type block struct {
	page int
	y    float64
	line *line
	tbl  *table.Region
}

// PageMarkdown is one page's rendered document.
type PageMarkdown struct {
	Page     int
	Markdown string
}

// ConvertPerPage renders each page independently, for OCR fusion.
func ConvertPerPage(pages []extract.PageResult, opts Options) []PageMarkdown {
	var out []PageMarkdown
	for i, page := range pages {
		pageNum := pageNumOf(page, i)
		md, _ := Convert([]extract.PageResult{page}, opts)
		out = append(out, PageMarkdown{Page: pageNum, Markdown: md})
	}
	return out
}

// Convert renders items from every page into one Markdown document.
func Convert(pages []extract.PageResult, opts Options) (string, Complexity) {
	var blocks []block
	var pagesWithCols []int
	pagesWithTables := map[int]bool{}
	for i, page := range pages {
		pageNum := pageNumOf(page, i)
		regions, remainder := table.Detect(page)
		for r := range regions {
			pagesWithTables[pageNum] = true
			region := regions[r]
			blocks = append(blocks, block{page: pageNum, y: region.RowY[len(region.RowY)-1], tbl: &region})
		}
		for _, ln := range groupLines(remainder, pageNum) {
			blocks = append(blocks, block{page: pageNum, y: ln.y, line: &ln})
		}
		if hasColumns(page.Items) {
			pagesWithCols = append(pagesWithCols, pageNum)
		}
	}
	if len(blocks) == 0 {
		return "", Complexity{}
	}
	sort.SliceStable(blocks, func(a, b int) bool {
		if blocks[a].page != blocks[b].page {
			return blocks[a].page < blocks[b].page
		}
		return blocks[a].y > blocks[b].y
	})
	sort.Ints(pagesWithCols)

	furniture := map[string]bool{}
	if opts.StripFurniture && len(pages) > 1 {
		furniture = detectFurniture(pages)
	}

	lines := linesOf(blocks)
	base := baseFontSize(lines)
	tiers := headingTiers(lines, base)

	paragraphThreshold := computeParagraphThreshold(lines, base)
	var out []string
	var paragraph []string
	var prevLine line
	flushParagraph := func() {
		if len(paragraph) > 0 {
			out = append(out, strings.Join(paragraph, " "))
			paragraph = nil
		}
	}
	var codeRun []line
	flushCode := func() {
		if len(codeRun) == 0 {
			return
		}
		var body []string
		for _, cl := range codeRun {
			body = append(body, plainLineText(cl))
		}
		out = append(out, "```\n"+strings.Join(body, "\n")+"\n```")
		codeRun = nil
	}

	for _, blk := range blocks {
		if blk.tbl != nil {
			flushParagraph()
			flushCode()
			if rendered := blk.tbl.Render(); rendered != "" {
				out = append(out, rendered)
			}
			prevLine = line{page: blk.page, y: blk.y}
			continue
		}
		ln := *blk.line
		key := furnitureKey(ln)
		if furniture[key] {
			continue
		}
		if opts.RemovePageNums && isPageNumber(ln) {
			continue
		}
		text := plainLineText(ln)
		if strings.TrimSpace(text) == "" {
			continue
		}
		if opts.DetectCode && isMonoLine(ln) {
			flushParagraph()
			codeRun = append(codeRun, ln)
			prevLine = ln
			continue
		}
		flushCode()

		if opts.DetectHeadings {
			if level, ok := headingLevel(ln, tiers); ok {
				flushParagraph()
				out = append(out, strings.Repeat("#", level)+" "+renderItems(ln.items, opts, base))
				prevLine = ln
				continue
			}
		}
		if opts.DetectLists {
			if marker, rest, ok := listMarker(ln); ok {
				flushParagraph()
				out = append(out, strings.TrimSpace(marker+" "+renderItems(rest, opts, base)))
				prevLine = ln
				continue
			}
		}
		joined := false
		if len(paragraph) > 0 && prevLine.page == ln.page {
			gap := prevLine.y - ln.y
			if gap > 0 && gap <= paragraphThreshold {
				paragraph = append(paragraph, renderItems(ln.items, opts, base))
				joined = true
			}
		}
		if !joined {
			flushParagraph()
			paragraph = []string{renderItems(ln.items, opts, base)}
		}
		prevLine = ln
	}
	flushParagraph()
	flushCode()

	md := strings.Join(out, "\n\n") + "\n"
	if opts.FixHyphenation {
		md = fixHyphenation(md)
	}
	md = postprocess(md)

	var tablePages []int
	for p := range pagesWithTables {
		tablePages = append(tablePages, p)
	}
	sort.Ints(tablePages)
	complexity := Complexity{PagesWithCols: pagesWithCols, PagesWithTables: tablePages}
	complexity.IsComplex = len(pagesWithCols) > 0 || len(tablePages) > 0
	return md, complexity
}

func linesOf(blocks []block) []line {
	var lines []line
	for _, blk := range blocks {
		if blk.line != nil {
			lines = append(lines, *blk.line)
		}
	}
	return lines
}

func groupLines(items []extract.TextItem, page int) []line {
	var text []*extract.TextItem
	for i := range items {
		if items[i].ItemType == extract.ItemText {
			text = append(text, &items[i])
		}
	}
	sort.SliceStable(text, func(a, b int) bool {
		la, lb := text[a].Y-text[a].BaselineShift, text[b].Y-text[b].BaselineShift
		if la != lb {
			return la > lb
		}
		return text[a].X < text[b].X
	})
	var grouped []line
	for _, it := range text {
		itLineY := it.Y - it.BaselineShift
		placed := false
		for g := range grouped {
			headLineY := grouped[g].y - grouped[g].items[0].BaselineShift
			tol := math.Max(2.0, math.Max(math.Abs(grouped[g].items[0].FontSize), math.Abs(it.FontSize))*0.45)
			if math.Abs(itLineY-headLineY) <= tol {
				grouped[g].items = append(grouped[g].items, *it)
				placed = true
				break
			}
		}
		if !placed {
			grouped = append(grouped, line{page: page, items: []extract.TextItem{*it}, y: itLineY, x: it.X})
		}
	}
	for g := range grouped {
		gi := grouped[g].items
		sort.SliceStable(gi, func(a, b int) bool {
			if math.Abs(gi[a].Y-gi[b].Y) > 2.5 && gi[a].BaselineShift == 0 && gi[b].BaselineShift == 0 {
				return gi[a].Y > gi[b].Y
			}
			return gi[a].X < gi[b].X
		})
	}
	return grouped
}

func hasColumns(items []extract.TextItem) bool {
	var text []extract.TextItem
	for _, it := range items {
		if it.ItemType == extract.ItemText && it.Rotation == 0 {
			text = append(text, it)
		}
	}
	if len(text) < 6 {
		return false
	}
	gaps := columnGaps(text)
	return len(gaps) > 0
}

// columnGaps finds x positions where items split into clusters with wide,
// repeatedly-used gutters.
func columnGaps(text []extract.TextItem) []float64 {
	xs := make([]float64, 0, len(text))
	maxRight := 0.0
	for _, it := range text {
		xs = append(xs, it.X)
		maxRight = math.Max(maxRight, it.X+it.Width)
	}
	sort.Float64s(xs)
	if maxRight <= 0 {
		return nil
	}
	var candidates []float64
	for i := 1; i < len(xs); i++ {
		if xs[i]-xs[i-1] > 40 && xs[i-1] > maxRight*0.2 && xs[i] < maxRight*0.85 {
			candidates = append(candidates, xs[i]-xs[i-1])
		}
	}
	return candidates
}

func computeParagraphThreshold(lines []line, base float64) float64 {
	fallback := base * 1.8
	var gaps []float64
	prevPage, prevY := -1, 0.0
	for _, ln := range lines {
		if prevPage == ln.page {
			gap := prevY - ln.y
			if gap > 0 && gap < base*10 {
				gaps = append(gaps, gap)
			}
		}
		prevPage, prevY = ln.page, ln.y
	}
	if len(gaps) < 5 {
		return fallback
	}
	sort.Float64s(gaps)
	median := gaps[len(gaps)/2]
	return math.Max(median*1.3, base*1.5)
}

func baseFontSize(lines []line) float64 {
	counts := map[int]int{}
	for _, ln := range lines {
		if len(ln.items) == 0 {
			continue
		}
		fs := math.Abs(ln.items[0].FontSize)
		if fs >= 9 {
			counts[int(math.Round(fs*10))]++
		}
	}
	bestCount, bestKey := 0, 120
	for k, c := range counts {
		if c > bestCount || (c == bestCount && k > bestKey) {
			bestCount, bestKey = c, k
		}
	}
	return float64(bestKey) / 10
}

func headingTiers(lines []line, base float64) []float64 {
	var sizes []float64
	for _, ln := range lines {
		if len(ln.items) == 0 {
			continue
		}
		first := ln.items[0]
		if first.FontSize/base < 1.2 {
			continue
		}
		text := strings.TrimSpace(plainLineText(ln))
		if text == "" || !strings.ContainsFunc(text, unicode.IsLetter) {
			continue
		}
		sizes = append(sizes, first.FontSize)
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(sizes)))
	var tiers []float64
	for _, size := range sizes {
		if len(tiers) > 0 && math.Abs(tiers[len(tiers)-1]-size) < 0.5 {
			continue
		}
		tiers = append(tiers, size)
		if len(tiers) >= 4 {
			break
		}
	}
	sort.Float64s(tiers)
	return tiers
}

func headingLevel(ln line, tiers []float64) (int, bool) {
	if len(ln.items) == 0 {
		return 0, false
	}
	fs := math.Abs(ln.items[0].FontSize)
	for i, tier := range tiers {
		if fs >= tier-0.05 {
			return i + 1, true
		}
	}
	return 0, false
}

func isMonoLine(ln line) bool {
	for _, it := range ln.items {
		name := strings.ToLower(it.Font)
		if strings.Contains(name, "courier") || strings.Contains(name, "mono") || strings.Contains(name, "consol") {
			return true
		}
	}
	return false
}

func listMarker(ln line) (string, []extract.TextItem, bool) {
	if len(ln.items) == 0 {
		return "", nil, false
	}
	first := ln.items[0]
	text := first.Text
	trimmed := strings.TrimLeft(text, " ")
	if trimmed == text && len(ln.items) > 0 && first.X > lnMinX(ln)+math.Abs(first.FontSize)*1.2 {
		return "- ", ln.items, true
	}
	for _, b := range []string{"•", "·", "◦", "○", "●", "-", "*"} {
		if strings.HasPrefix(trimmed, b+" ") || trimmed == b {
			return "- ", trimFirstRun(ln, len(b)), true
		}
	}
	if m := matchesNumberMarker(trimmed); m > 0 {
		return "", trimFirstRun(ln, m), true
	}
	return "", nil, false
}

func lnMinX(ln line) float64 {
	m := math.Inf(1)
	for _, it := range ln.items {
		m = math.Min(m, it.X)
	}
	return m
}

func matchesNumberMarker(text string) int {
	i := 0
	for i < len(text) && text[i] >= '0' && text[i] <= '9' {
		i++
	}
	if i == 0 || i > 3 {
		return 0
	}
	rest := text[i:]
	for _, suffix := range []string{". ", ") ", " "} {
		if strings.HasPrefix(rest, suffix) {
			return i + len(suffix)
		}
	}
	return 0
}

func trimFirstRun(ln line, n int) []extract.TextItem {
	out := make([]extract.TextItem, len(ln.items))
	copy(out, ln.items)
	if len(out) == 0 {
		return out
	}
	out[0].Text = out[0].Text[min(n, len(out[0].Text)):]
	return out
}

func isPageNumber(ln line) bool {
	text := strings.TrimSpace(plainLineText(ln))
	if len(text) > 12 {
		return false
	}
	for _, c := range text {
		if !unicode.IsDigit(c) && c != '/' && c != ' ' && c != '-' && c != ',' {
			return false
		}
	}
	return len(text) > 0
}

func detectFurniture(pages []extract.PageResult) map[string]bool {
	type edgeKey struct{ text, position string }
	counts := map[edgeKey]int{}
	for i, page := range pages {
		lines := groupLines(page.Items, pageNumOf(page, i))
		if len(lines) == 0 {
			continue
		}
		first, last := lines[0], lines[len(lines)-1]
		if t := normalizeFurniture(plainLineText(first)); t != "" {
			counts[edgeKey{t, "top"}]++
		}
		if t := normalizeFurniture(plainLineText(last)); t != "" {
			counts[edgeKey{t, "bottom"}]++
		}
	}
	out := map[string]bool{}
	for key, count := range counts {
		if count >= 2 && count >= len(pages)/2 {
			out[key.text+"|"+key.position] = true
		}
	}
	return out
}

func normalizeFurniture(text string) string {
	return strings.Join(strings.Fields(strings.ToLower(text)), " ")
}

func furnitureKey(ln line) string {
	if len(ln.items) == 0 {
		return ""
	}
	return normalizeFurniture(plainLineText(ln))
}

func plainLineText(ln line) string {
	var parts []string
	for _, it := range ln.items {
		parts = append(parts, it.Text)
	}
	joined := strings.Join(parts, " ")
	return strings.Join(strings.Fields(joined), " ")
}

func renderItems(items []extract.TextItem, opts Options, base float64) string {
	var sb strings.Builder
	prevShift := 0.0
	var last rune
	for _, it := range items {
		text := it.Text
		if text == "" {
			continue
		}
		if it.ItemType == extract.ItemLink && opts.IncludeLinks && it.URL != "" {
			text = "[" + text + "](" + it.URL + ")"
		}
		if it.BaselineShift > 0.5 {
			text = "<sup>" + text + "</sup>"
		} else if it.BaselineShift < -0.5 {
			text = "<sub>" + text + "</sub>"
		}
		if it.IsBold && it.IsItalic {
			text = "***" + text + "***"
		} else if it.IsBold {
			text = "**" + text + "**"
		} else if it.IsItalic {
			text = "*" + text + "*"
		}
		if it.BaselineShift == 0 && prevShift == 0 {
			if last != 0 && !unicode.IsSpace(last) && !isJoinPunctMD(firstChar(text)) {
				sb.WriteByte(' ')
			}
		}
		sb.WriteString(text)
		last = lastChar(text)
		prevShift = it.BaselineShift
	}
	return sb.String()
}

func lastChar(s string) rune {
	var last rune
	for _, c := range s {
		last = c
	}
	return last
}

func firstChar(s string) rune {
	for _, c := range s {
		return c
	}
	return 0
}

func isJoinPunctMD(c rune) bool {
	switch c {
	case '.', ',', ';', ':', ')', ']', '!', '?', '<':
		return true
	}
	return false
}

func fixHyphenation(md string) string {
	lines := strings.Split(md, "\n")
	for i := 0; i+1 < len(lines); i++ {
		cur := strings.TrimRight(lines[i], " ")
		next := strings.TrimLeft(lines[i+1], " ")
		if strings.HasSuffix(cur, "-") && len(next) > 0 && unicode.IsLower(firstChar(next)) && unicode.IsLower(rune(cur[len(cur)-2])) {
			lines[i] = cur[:len(cur)-1]
			lines[i+1] = next
		}
	}
	return strings.Join(lines, "\n")
}

func postprocess(md string) string {
	lines := strings.Split(md, "\n")
	var out []string
	blank := 0
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, strings.TrimRight(l, " "))
	}
	return strings.TrimLeft(strings.Join(out, "\n"), "\n")
}
