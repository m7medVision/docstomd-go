package extract

import (
	"math"

	"github.com/m7medVision/docstomd-go/internal/pdf/parse"
)

// Extract runs the content-stream interpreter over every page and returns
// items in the visible-page-box frame.
func Extract(doc *parse.Document) []PageResult {
	pages := doc.Pages()
	results := make([]PageResult, 0, len(pages))
	for i, pageRef := range pages {
		pageNum := i + 1
		pageDict := doc.PageDict(pageRef)
		if pageDict == nil {
			results = append(results, PageResult{})
			continue
		}
		resources := doc.PageResources(pageRef)
		fonts := map[string]*fontContext{}
		if fontDict, ok := dictOf(doc, firstNonNil(resources, "Font")); ok {
			for name, value := range fontDict {
				fd, ok := dictOf(doc, value)
				if !ok {
					continue
				}
				fonts[name] = buildFontContext(doc, fd)
			}
		}
		it := newInterp(doc, pageNum, resources, fonts)
		for _, contentRef := range doc.PageContents(pageRef) {
			obj, err := doc.GetObject(contentRef.Num)
			if err != nil {
				continue
			}
			if stm, ok := obj.(*parse.Stream); ok {
				content, _ := doc.StreamData(stm)
				it.run(content)
			}
		}
		box := resolvePageBox(doc, pageDict)
		shiftItems(it.items, box)
		shiftRects(it.rects, box)
		shiftLines(it.lines, box)
		detectUnderlinesRaw(it.items, it.painted, it.strokeLines)
		textItems := mergeSubscriptItems(mergeTextItems(it.items))
		linkItems := extractLinks(doc, pageDict, box, pageNum)
		items := textItems
		items = append(items, linkItems...)
		hasIssues := false
		for _, item := range items {
			if !hasIssues && stringsContainsRune(item.Text, 0xFFFD) {
				hasIssues = true
			}
		}
		results = append(results, PageResult{Items: items, Rects: it.rects, Lines: it.lines, HasEncodingIssues: hasIssues})
	}
	return results
}

func firstNonNil(scopes []map[string]any, key string) any {
	for _, res := range scopes {
		if v, ok := res[key]; ok && v != nil {
			return v
		}
	}
	return nil
}

func stringsContainsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}

func shiftItems(items []TextItem, box pageBox) {
	for i := range items {
		items[i].X -= box.x0
		items[i].Y -= box.y0
	}
}

func shiftRects(rects []Rect, box pageBox) {
	for i := range rects {
		rects[i].X -= box.x0
		rects[i].Y -= box.y0
	}
}

func shiftLines(lines []Line, box pageBox) {
	for i := range lines {
		lines[i].X1 -= box.x0
		lines[i].Y1 -= box.y0
		lines[i].X2 -= box.x0
		lines[i].Y2 -= box.y0
	}
}

func extractLinks(doc *parse.Document, pageDict map[string]any, box pageBox, pageNum int) []TextItem {
	annots, ok := docResolveArr(doc, pageDict["Annots"])
	if !ok {
		return nil
	}
	var out []TextItem
	for _, a := range annots {
		dict, ok := dictOf(doc, a)
		if !ok || nameOf(dict["Subtype"]) != "Link" {
			continue
		}
		rect, ok := linkRect(doc, dict)
		if !ok {
			continue
		}
		url := ""
		if action, ok := dictOf(doc, dict["A"]); ok {
			if uri, ok := doc.Resolve(action["URI"]).([]byte); ok {
				url = string(uri)
			}
		}
		out = append(out, TextItem{
			Text:         url,
			Page:         pageNum,
			X:            rect[0] - box.x0,
			Y:            rect[1] - box.y0,
			Width:        rect[2] - rect[0],
			Height:       rect[3] - rect[1],
			AdvanceKnown: true,
			ItemType:     ItemLink,
			URL:          url,
		})
	}
	return out
}

func linkRect(doc *parse.Document, dict map[string]any) ([4]float64, bool) {
	arr, ok := docResolveArr(doc, dict["Rect"])
	if !ok || len(arr) < 4 {
		return [4]float64{}, false
	}
	var vals [4]float64
	for i := 0; i < 4; i++ {
		v, ok := number(doc.Resolve(arr[i]))
		if !ok {
			return [4]float64{}, false
		}
		vals[i] = v
	}
	return [4]float64{
		math.Min(vals[0], vals[2]),
		math.Min(vals[1], vals[3]),
		math.Max(vals[0], vals[2]),
		math.Max(vals[1], vals[3]),
	}, true
}

// detectUnderlinesRaw marks pre-merge runs whose box a thin painted rule
// sits under (or across, for strikeout); the flags are merge boundaries.
// Rules repeating at three or more vertical levels over one x-span are table
// ruling and discarded.
func detectUnderlinesRaw(items []TextItem, painted []Rect, strokeLines []Line) {
	type rule struct {
		x0, x1, y, thickness float64
		page                 int
	}
	var rules []rule
	for _, r := range painted {
		if r.Height <= 2.0 {
			rules = append(rules, rule{r.X, r.X + r.Width, r.Y + r.Height/2, r.Height, r.Page})
		}
	}
	for _, l := range strokeLines {
		if l.Y1 == l.Y2 {
			rules = append(rules, rule{math.Min(l.X1, l.X2), math.Max(l.X1, l.X2), l.Y1, 0.75, l.Page})
		}
	}
	if len(rules) == 0 {
		return
	}
	kept := rules[:0]
	for i, r := range rules {
		repeated := 0
		levels := map[float64]bool{}
		for j, other := range rules {
			if i == j {
				continue
			}
			if other.page != r.page {
				continue
			}
			overlap := math.Min(r.x1, other.x1) - math.Max(r.x0, other.x0)
			minWidth := math.Min(r.x1-r.x0, other.x1-other.x0)
			if minWidth <= 0 {
				continue
			}
			wRatio := math.Max(r.x1-r.x0, other.x1-other.x0) / math.Max(0.01, math.Min(r.x1-r.x0, other.x1-other.x0))
			if overlap >= minWidth*0.8 && wRatio <= 1.5 && math.Abs(other.y-r.y) > 2.0 {
				levels[other.y] = true
			}
		}
		repeated = len(levels)
		if repeated < 2 {
			kept = append(kept, r)
		}
	}
	rules = kept

	for i := range items {
		item := &items[i]
		if item.ItemType != ItemText || item.Height <= 0 {
			continue
		}
		fs := math.Abs(item.FontSize)
		if fs <= 0 {
			continue
		}
		for _, r := range rules {
			if r.page != item.Page {
				continue
			}
			overlap := math.Min(r.x1, item.X+item.Width) - math.Max(r.x0, item.X)
			if item.Width <= 0 || overlap < item.Width*0.6 {
				continue
			}
			if r.y >= item.Y-fs*0.30 && r.y <= item.Y+fs*0.10 {
				item.IsUnderline = true
			}
			if r.y >= item.Y+fs*0.20 && r.y <= item.Y+fs*0.50 {
				item.IsStrikeout = true
			}
		}
	}
}
