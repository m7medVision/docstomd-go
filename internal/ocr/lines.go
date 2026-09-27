package ocr

import (
	"math"
	"slices"
	"strings"

	"github.com/m7medVision/docstomd-go/internal/pdf/extract"
	"github.com/m7medVision/docstomd-go/internal/pdf/markdown"
)

// Rect is a line box in the result's coordinate space: origin at the top-left
// corner, x to the right, y down.
type Rect struct {
	X0 float64 `json:"x0"`
	Y0 float64 `json:"y0"`
	X1 float64 `json:"x1"`
	Y1 float64 `json:"y1"`
}

// Line is one recognized text line.
type Line struct {
	Text       string  `json:"text"`
	Box        Rect    `json:"box"`
	Confidence float64 `json:"confidence,omitempty"`
}

// lineHeightPerPoint is the assumed ratio of a detected line box's height to
// the font size it was set in (ascenders plus descenders plus box padding).
const lineHeightPerPoint = 1.2

// sizeClusterTolerance merges line heights within this ratio of the smallest
// height in the cluster into one font size, so detector jitter does not
// invent heading tiers.
const sizeClusterTolerance = 1.15

// LineItems turns recognized lines into extraction items in PDF page space
// (points, origin bottom-left, y up) so they can go through the same
// Markdown rules as native pages. pageW and pageH are the page size in
// points; zero means the lines are already in points. Font size is estimated
// from box height, clustered and rounded to whole points. Font name, styles
// and tables are not recovered.
func LineItems(result PageResult, pageW, pageH float64) []extract.TextItem {
	sx, sy := 1.0, 1.0
	if pageW > 0 && result.Width > 0 {
		sx = pageW / result.Width
	}
	if pageH > 0 && result.Height > 0 {
		sy = pageH / result.Height
	}
	height := pageH
	if height <= 0 {
		height = result.Height * sy
	}
	var lines []Line
	var heights []float64
	for _, ln := range result.Lines {
		text := strings.Join(strings.Fields(ln.Text), " ")
		if text == "" || !validBox(ln.Box) {
			continue
		}
		ln.Text = text
		lines = append(lines, ln)
		heights = append(heights, math.Abs(ln.Box.Y1-ln.Box.Y0)*sy/lineHeightPerPoint)
	}
	sizes := clusterSizes(heights)
	items := make([]extract.TextItem, 0, len(lines))
	for i, ln := range lines {
		x0, x1 := math.Min(ln.Box.X0, ln.Box.X1), math.Max(ln.Box.X0, ln.Box.X1)
		bottom := math.Max(ln.Box.Y0, ln.Box.Y1)
		items = append(items, extract.TextItem{
			Text:         ln.Text,
			Page:         result.Page,
			X:            x0 * sx,
			Y:            height - bottom*sy,
			Width:        (x1 - x0) * sx,
			Height:       sizes[i],
			FontSize:     sizes[i],
			AdvanceKnown: true,
			ItemType:     extract.ItemText,
		})
	}
	return items
}

func validBox(b Rect) bool {
	for _, v := range []float64{b.X0, b.Y0, b.X1, b.Y1} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return b.Y1 != b.Y0 && b.X1 != b.X0
}

// clusterSizes maps each height to its cluster's median, rounded to whole
// points (never below 1).
func clusterSizes(heights []float64) []float64 {
	order := make([]int, len(heights))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int {
		switch {
		case heights[a] < heights[b]:
			return -1
		case heights[a] > heights[b]:
			return 1
		}
		return 0
	})
	sizes := make([]float64, len(heights))
	for start := 0; start < len(order); {
		end := start + 1
		for end < len(order) && heights[order[end]] <= heights[order[start]]*sizeClusterTolerance {
			end++
		}
		median := heights[order[(start+end)/2]]
		size := math.Max(1, math.Round(median))
		for _, idx := range order[start:end] {
			sizes[idx] = size
		}
		start = end
	}
	return sizes
}

// LinesMarkdown renders a line-based result with the PDF Markdown rules.
func LinesMarkdown(result PageResult, pageW, pageH float64, opts markdown.Options) string {
	page := extract.PageResult{Width: pageW, Height: pageH, Items: LineItems(result, pageW, pageH)}
	if len(page.Items) == 0 {
		return ""
	}
	return strings.TrimSpace(markdown.ConvertPerPage([]extract.PageResult{page}, opts)[0].Markdown)
}

// linesConfidence is the mean of the lines' positive confidences, or 0 when
// none reports one.
func linesConfidence(lines []Line) float64 {
	sum, n := 0.0, 0
	for _, ln := range lines {
		if ln.Confidence > 0 {
			sum += ln.Confidence
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}
