// Package table detects gridded tables from extraction geometry and assigns
// text items to cells.
package table

import (
	"math"
	"sort"
	"strings"

	"github.com/m7medVision/docstomd-go/internal/pdf/extract"
)

type Region struct {
	Page           int
	X0, X1, Y0, Y1 float64
	ColX, RowY     []float64
	Cells          [][]string
}

func (r *Region) contains(item extract.TextItem) bool {
	cx, cy := item.X+item.Width/2, item.Y+item.Height/2
	return cx >= r.X0 && cx <= r.X1 && cy >= r.Y0 && cy <= r.Y1
}

const (
	edgeClusterTol = 3.0
	minCellRects   = 6
)

// Detect finds ruled and band-ruled tables on a page and fills cells with
// item text; header rects give column structure, item y-bands extend data
// rows. Items consumed by a table are returned as the remainder.
func Detect(page extract.PageResult) ([]Region, []extract.TextItem) {
	cellRects, verticals := classifyRects(page.Rects)
	regions := gridFromRects(cellRects, page)
	for _, band := range gridFromBands(cellRects, verticals, page) {
		overlaps := false
		for _, existing := range regions {
			if band.X0 >= existing.X0-2 && band.X1 <= existing.X1+2 && band.Y0 >= existing.Y0-2 && band.Y1 <= existing.Y1+2 {
				overlaps = true
				break
			}
		}
		if !overlaps {
			regions = append(regions, band)
		}
	}
	if len(regions) == 0 {
		return nil, page.Items
	}
	var remainder []extract.TextItem
	used := map[*extract.TextItem]bool{}
	var valid []Region
	for i := range regions {
		extendDataRows(&regions[i], page.Items)
		if len(regions[i].RowY) >= 3 {
			valid = append(valid, regions[i])
			continue
		}
	}
	for i := range valid {
		fillCells(&valid[i], page.Items, used)
	}
	regions = valid
	for i := range page.Items {
		if !used[&page.Items[i]] {
			remainder = append(remainder, page.Items[i])
		}
	}
	return regions, remainder
}

func classifyRects(rects []extract.Rect) (cells, verticals []extract.Rect) {
	for _, r := range rects {
		switch {
		case r.Width <= 3.0 && r.Height > 3.0:
			verticals = append(verticals, r)
		case r.Width > 3.0 && r.Height > 3.0:
			cells = append(cells, r)
		}
	}
	return cells, verticals
}

// gridFromBands handles tables drawn as stacked full-width row bands with
// thin vertical separators: rects wider than 80pt that sit alone on their
// scan line.
func gridFromBands(cells, verticals []extract.Rect, page extract.PageResult) []Region {
	var bands []extract.Rect
	for _, r := range cells {
		if r.Width > 80.0 && r.Height <= 45.0 {
			bands = append(bands, r)
		}
	}
	if len(bands) < 3 {
		return nil
	}
	sort.Slice(bands, func(a, b int) bool {
		if bands[a].Y != bands[b].Y {
			return bands[a].Y > bands[b].Y
		}
		return bands[a].X < bands[b].X
	})
	var regions []Region
	i := 0
	for i < len(bands) {
		x0, x1 := bands[i].X, bands[i].X+bands[i].Width
		j := i + 1
		for j < len(bands) && bands[j].Y < bands[j-1].Y-1 &&
			math.Abs(bands[j].Y+bands[j].Height-bands[j-1].Y) < bands[j].Height*0.6 &&
			bands[j].X >= x0-2 && bands[j].X+bands[j].Width <= x1+2 {
			j++
		}
		if j-i >= 3 {
			var rowY []float64
			for k := i; k < j; k++ {
				rowY = append(rowY, bands[k].Y, bands[k].Y+bands[k].Height)
			}
			sort.Float64s(rowY)
			var cols []float64
			cols = append(cols, x0)
			for _, v := range verticals {
				if v.X > x0+2 && v.X+v.Width < x1-2 {
					cols = append(cols, v.X)
				}
			}
			cols = append(cols, x1)
			sort.Float64s(cols)
			if len(cols) >= 3 {
				regions = append(regions, Region{
					X0: x0, X1: x1,
					Y0: rowY[0], Y1: rowY[len(rowY)-1],
					ColX: cols, RowY: rowY,
				})
			}
		}
		i = j
	}
	return regions
}

func gridFromRects(cells []extract.Rect, page extract.PageResult) []Region {
	if len(cells) < 4 {
		return nil
	}
	rows := clusterRectRows(cells)
	var regions []Region
	i := 0
	for i < len(rows) {
		if len(rows[i].rects) < 2 {
			i++
			continue
		}
		j := i + 1
		for j < len(rows) && len(rows[j].rects) >= 2 {
			a0, a1 := rows[i].span()
			b0, b1 := rows[j].span()
			if xOverlap([2]float64{a0, a1}, [2]float64{b0, b1}) <= 0.6 {
				break
			}
			gap := rows[j-1].bottom() - rows[j].top()
			rowH := rows[j-1].bottom() - rows[j-1].top()
			if gap < -2 || gap > math.Max(40, rowH*3) {
				break
			}
			j++
		}
		group := rows[i:j]
		if len(group) >= 2 || (len(group) == 1 && len(group[0].rects) >= 3) {
			if region := regionFromRows(group, pageNum(page)); region != nil {
				regions = append(regions, *region)
			}
		}
		i = j
	}
	return regions
}

type rectRow struct {
	rects []extract.Rect
}

func (r rectRow) top() float64 { return r.rects[0].Y }
func (r rectRow) bottom() float64 {
	b := r.rects[0].Y + r.rects[0].Height
	for _, rc := range r.rects {
		b = math.Max(b, rc.Y+rc.Height)
	}
	return b
}
func (r rectRow) span() (float64, float64) {
	x0 := math.Inf(1)
	x1 := math.Inf(-1)
	for _, rc := range r.rects {
		x0 = math.Min(x0, rc.X)
		x1 = math.Max(x1, rc.X+rc.Width)
	}
	return x0, x1
}

func xOverlap(a, b [2]float64) float64 {
	lo := math.Max(a[0], b[0])
	hi := math.Min(a[1], b[1])
	if hi <= lo {
		return 0
	}
	minW := math.Min(a[1]-a[0], b[1]-b[0])
	if minW <= 0 {
		return 0
	}
	return (hi - lo) / minW
}

func clusterRectRows(cells []extract.Rect) []rectRow {
	sorted := make([]extract.Rect, len(cells))
	copy(sorted, cells)
	sort.Slice(sorted, func(a, b int) bool {
		if sorted[a].Y != sorted[b].Y {
			return sorted[a].Y > sorted[b].Y
		}
		return sorted[a].X < sorted[b].X
	})
	var rows []rectRow
	for _, rc := range sorted {
		if n := len(rows); n > 0 && math.Abs(rc.Y-rows[n-1].top()) <= 2.5 {
			rows[n-1].rects = append(rows[n-1].rects, rc)
			continue
		}
		rows = append(rows, rectRow{rects: []extract.Rect{rc}})
	}
	return rows
}

func regionFromRows(rows []rectRow, page int) *Region {
	rowSupport := len(rows) / 2
	if rowSupport < 2 {
		rowSupport = 2
	}
	var edges []float64
	for _, r := range rows {
		for _, rc := range r.rects {
			edges = append(edges, rc.X, rc.X+rc.Width)
		}
	}
	colX := boundariesWithSupport(edges, rowSupport)
	x0, x1 := rows[0].span()
	for _, r := range rows[1:] {
		a, b := r.span()
		x0 = math.Min(x0, a)
		x1 = math.Max(x1, b)
	}
	colX = append(colX, x0, x1)
	sort.Float64s(colX)
	if len(colX) < 3 {
		return nil
	}
	var rowY []float64
	for _, r := range rows {
		rowY = append(rowY, r.top(), r.bottom())
	}
	deduped := dedupeSorted(rowY, 1.5)
	return &Region{
		Page: page,
		X0:   colX[0], X1: colX[len(colX)-1],
		Y0: deduped[0], Y1: deduped[len(deduped)-1],
		ColX: colX, RowY: deduped,
	}
}

func pageNum(page extract.PageResult) int {
	if len(page.Items) > 0 {
		return page.Items[0].Page
	}
	if len(page.Rects) > 0 {
		return page.Rects[0].Page
	}
	if len(page.Lines) > 0 {
		return page.Lines[0].Page
	}
	return 1
}

func boundariesWithSupport(edges []float64, minSupport int) []float64 {
	type cluster struct {
		value float64
		count int
	}
	var clusters []cluster
	for _, e := range edges {
		if n := len(clusters); n > 0 && math.Abs(e-clusters[n-1].value) <= edgeClusterTol {
			clusters[n-1].count++
			continue
		}
		clusters = append(clusters, cluster{value: e, count: 1})
	}
	var out []float64
	for _, c := range clusters {
		if c.count >= minSupport {
			out = append(out, c.value)
		}
	}
	return dedupeSorted(out, edgeClusterTol)
}

func fillCells(region *Region, items []extract.TextItem, used map[*extract.TextItem]bool) {
	rows := len(region.RowY) - 1
	cols := len(region.ColX) - 1
	if rows <= 0 || cols <= 0 {
		return
	}
	region.Cells = make([][]string, rows)
	for r := range region.Cells {
		region.Cells[r] = make([]string, cols)
	}
	rowIndex := func(yBand int) int { return rows - 1 - yBand }
	type cellItem struct {
		row, col int
		item     extract.TextItem
	}
	var assignments []cellItem
	for i := range items {
		it := items[i]
		if it.ItemType != extract.ItemText || !region.contains(it) {
			continue
		}
		cx, cy := it.X+it.Width/2, it.Y+it.Height/2
		col := -1
		for c := range cols {
			if cx >= region.ColX[c] && cx < region.ColX[c+1] {
				col = c
				break
			}
		}
		row := -1
		for r := 0; r < rows; r++ {
			if cy >= region.RowY[r] && cy < region.RowY[r+1] {
				row = r
				break
			}
		}
		if row < 0 || col < 0 {
			continue
		}
		assignments = append(assignments, cellItem{row: rowIndex(row), col: col, item: it})
		used[&items[i]] = true
	}
	sort.SliceStable(assignments, func(a, b int) bool {
		if assignments[a].row != assignments[b].row {
			return assignments[a].row < assignments[b].row
		}
		if assignments[a].col != assignments[b].col {
			return assignments[a].col < assignments[b].col
		}
		return assignments[a].item.X < assignments[b].item.X
	})
	for _, a := range assignments {
		text := a.item.Text
		if region.Cells[a.row][a.col] == "" {
			region.Cells[a.row][a.col] = text
		} else {
			region.Cells[a.row][a.col] += " " + text
		}
	}
	for r := range region.Cells {
		distributeFinancialTokens(region.Cells[r])
		for c := range region.Cells[r] {
			region.Cells[r][c] = trimCell(region.Cells[r][c])
		}
	}
}

// distributeFinancialTokens spreads a cell holding several whitespace-run
// numeric tokens into the empty cells to its right — a merged numeric cell
// painted across column borders — one token per column.
func distributeFinancialTokens(row []string) {
	for c := range row {
		tokens := strings.Fields(row[c])
		if len(tokens) < 2 || !allNumeric(tokens) {
			continue
		}
		emptyRight := 0
		for k := c + 1; k < len(row) && row[k] == ""; k++ {
			emptyRight++
		}
		if emptyRight < len(tokens)-1 {
			continue
		}
		row[c] = tokens[0]
		k := c + 1
		for _, tok := range tokens[1:] {
			row[k] = tok
			k++
		}
	}
}

func allNumeric(tokens []string) bool {
	for _, tok := range tokens {
		hasDigit := false
		for _, r := range tok {
			if r >= '0' && r <= '9' {
				hasDigit = true
			} else if r != '.' && r != ',' && r != '%' && r != '$' && r != '-' && r != '+' {
				return false
			}
		}
		if !hasDigit {
			return false
		}
	}
	return true
}

func dedupeSorted(values []float64, tol float64) []float64 {
	sort.Float64s(values)
	var out []float64
	for _, v := range values {
		if len(out) == 0 || v-out[len(out)-1] > tol {
			out = append(out, v)
		}
	}
	return out
}

func trimCell(s string) string {
	out := make([]rune, 0, len(s))
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' {
			if !prevSpace && len(out) > 0 {
				out = append(out, ' ')
			}
			prevSpace = true
			continue
		}
		out = append(out, r)
		prevSpace = false
	}
	for len(out) > 0 && out[len(out)-1] == ' ' {
		out = out[:len(out)-1]
	}
	return string(out)
}

// Render produces the GFM pipe table for a region.
func (r *Region) Render() string {
	rows := len(r.Cells)
	if rows == 0 {
		return ""
	}
	cols := len(r.Cells[0])
	var out []string
	for i, row := range r.Cells {
		line := "|"
		any := false
		for _, cell := range row {
			if cell != "" {
				any = true
			}
			line += cell + "|"
		}
		if !any && len(out) > 0 {
			continue
		}
		out = append(out, line)
		if i == 0 {
			sep := "|"
			for c := 0; c < cols; c++ {
				sep += "---|"
			}
			out = append(out, sep)
		}
	}
	for len(out) > 2 && pipeRowIsEmpty(out[len(out)-1]) {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

func pipeRowIsEmpty(line string) bool {
	empty := true
	for _, c := range line {
		if c != '|' && c != ' ' {
			empty = false
		}
	}
	return empty
}

// extendDataRows grows a header-only grid downward with item y-bands: each
// band must place items into at least two distinct columns and stop at the
// first band that does not fit the column pattern.
func extendDataRows(region *Region, items []extract.TextItem) {
	if len(region.RowY) < 2 {
		return
	}
	cols := len(region.ColX) - 1
	var below []extract.TextItem
	for _, it := range items {
		if it.ItemType != extract.ItemText {
			continue
		}
		cy := it.Y + it.Height/2
		if cy < region.RowY[0] && cy > region.RowY[0]-200 && it.X+it.Width/2 >= region.X0 && it.X+it.Width/2 <= region.X1 {
			below = append(below, it)
		}
	}
	sort.SliceStable(below, func(a, b int) bool { return below[a].Y > below[b].Y })
	var bands [][]extract.TextItem
	for _, it := range below {
		if n := len(bands); n > 0 && math.Abs(bands[n-1][0].Y-it.Y) <= math.Max(3.0, math.Abs(it.FontSize)*0.5) {
			bands[n-1] = append(bands[n-1], it)
			continue
		}
		bands = append(bands, []extract.TextItem{it})
	}
	colWidth := (region.X1 - region.X0) / float64(cols)
	lastRowTop := 0.0
	for _, band := range bands {
		distinct := map[int]bool{}
		for _, it := range band {
			cx := it.X + it.Width/2
			for c := range cols {
				if cx >= region.ColX[c] && cx < region.ColX[c+1] {
					distinct[c] = true
				}
			}
		}
		if len(distinct) < 2 || len(band) > cols*2+2 {
			break
		}
		tooWide := false
		for _, it := range band {
			// Only an item starting outside column 0 trips the width
			// guard: a long left-column label keeps its band in the grid.
			if it.Width > colWidth*1.6 && it.X >= region.ColX[1] {
				tooWide = true
				break
			}
		}
		if tooWide {
			break
		}
		bandTop := band[0].Y + band[0].Height
		bandBottom := band[0].Y
		for _, it := range band {
			bandTop = math.Max(bandTop, it.Y+it.Height)
			bandBottom = math.Min(bandBottom, it.Y)
		}
		if lastRowTop > 0 && lastRowTop-bandBottom > band[0].Height*3 {
			break
		}
		region.RowY = append(region.RowY, bandTop, bandBottom)
		region.Y0 = math.Min(region.Y0, bandBottom)
		lastRowTop = bandTop
	}
	region.RowY = dedupeSorted(region.RowY, 1.5)
}
