package extract

import (
	"math"

	"github.com/m7medVision/docstomd-go/internal/pdf/parse"
)

// pageBox is the visible page box in raw user space: CropBox ∩ MediaBox when
// both exist, else whichever exists, else US Letter.
type pageBox struct{ x0, y0, x1, y1 float64 }

func resolvePageBox(doc *parse.Document, pageDict map[string]any) pageBox {
	media := boxOf(doc, pageDict["MediaBox"])
	crop := boxOf(doc, pageDict["CropBox"])
	switch {
	case media != nil && crop != nil:
		x0 := math.Max(media.x0, crop.x0)
		y0 := math.Max(media.y0, crop.y0)
		x1 := math.Min(media.x1, crop.x1)
		y1 := math.Min(media.y1, crop.y1)
		if x1 > x0 && y1 > y0 {
			return pageBox{x0, y0, x1, y1}
		}
		return *media
	case crop != nil:
		return *crop
	case media != nil:
		return *media
	}
	return pageBox{0, 0, 612, 792}
}

func boxOf(doc *parse.Document, obj any) *pageBox {
	arr, ok := doc.Resolve(obj).([]any)
	if !ok || len(arr) < 4 {
		return nil
	}
	vals := make([]float64, 4)
	for i := 0; i < 4; i++ {
		v, ok := number(doc.Resolve(arr[i]))
		if !ok || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil
		}
		vals[i] = v
	}
	x0, y0 := math.Min(vals[0], vals[2]), math.Min(vals[1], vals[3])
	x1, y1 := math.Max(vals[0], vals[2]), math.Max(vals[1], vals[3])
	if x1 <= x0 || y1 <= y0 {
		return nil
	}
	return &pageBox{x0, y0, x1, y1}
}
