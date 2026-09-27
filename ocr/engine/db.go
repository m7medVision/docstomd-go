package engine

import (
	"math"
	"sort"
)

type pt struct{ x, y float64 }

// quad is a text box: top-left, top-right, bottom-right, bottom-left.
type quad [4]pt

func (q quad) bounds() (x0, y0, x1, y1 float64) {
	x0, y0, x1, y1 = q[0].x, q[0].y, q[0].x, q[0].y
	for _, p := range q[1:] {
		x0, y0 = math.Min(x0, p.x), math.Min(y0, p.y)
		x1, y1 = math.Max(x1, p.x), math.Max(y1, p.y)
	}
	return
}

// size is the crop size: the longer of each pair of opposite edges.
func (q quad) size() (w, h float64) {
	w = math.Max(dist(q[0], q[1]), dist(q[3], q[2]))
	h = math.Max(dist(q[0], q[3]), dist(q[1], q[2]))
	return
}

func dist(a, b pt) float64 { return math.Hypot(a.x-b.x, a.y-b.y) }

// dbBoxes extracts text boxes from a DB probability map of size w×h (row
// stride stride), restricted to the valid region vw×vh, and scales them by
// (sx, sy) into source image coordinates clipped to imgW×imgH.
func dbBoxes(prob []float32, stride, vw, vh int, pp PostProcess, sx, sy float64, imgW, imgH int) []quad {
	minSize := pp.MinSize
	if minSize == 0 {
		minSize = 3
	}
	maxCand := pp.MaxCandidates
	if maxCand == 0 {
		maxCand = 1000
	}
	thresh := float32(pp.Thresh)
	label := make([]int32, vw*vh)
	var boxes []quad
	var queue []int
	next := int32(0)
	for start := range label {
		sx0, sy0 := start%vw, start/vw
		if label[start] != 0 || prob[sy0*stride+sx0] <= thresh {
			continue
		}
		next++
		if int(next) > maxCand {
			break
		}
		// Flood fill (8-connected), collecting the component's pixels.
		var points []pt
		queue = append(queue[:0], start)
		label[start] = next
		for len(queue) > 0 {
			i := queue[len(queue)-1]
			queue = queue[:len(queue)-1]
			x, y := i%vw, i/vw
			points = append(points, pt{float64(x), float64(y)})
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					nx, ny := x+dx, y+dy
					if nx < 0 || ny < 0 || nx >= vw || ny >= vh {
						continue
					}
					j := ny*vw + nx
					if label[j] == 0 && prob[ny*stride+nx] > thresh {
						label[j] = next
						queue = append(queue, j)
					}
				}
			}
		}
		if len(points) < 4 {
			continue
		}
		rect, short := minAreaRect(convexHull(points))
		if short < minSize {
			continue
		}
		if boxScore(prob, stride, vw, vh, rect) < pp.BoxThresh {
			continue
		}
		rect, short = unclip(rect, pp.UnclipRatio)
		if short < minSize+2 {
			continue
		}
		var q quad
		for k, p := range rect {
			q[k] = pt{
				x: math.Max(0, math.Min(math.Round(p.x*sx), float64(imgW))),
				y: math.Max(0, math.Min(math.Round(p.y*sy), float64(imgH))),
			}
		}
		boxes = append(boxes, q)
	}
	sortBoxes(boxes)
	return boxes
}

// convexHull is Andrew's monotone chain.
func convexHull(points []pt) []pt {
	sort.Slice(points, func(a, b int) bool {
		if points[a].x != points[b].x {
			return points[a].x < points[b].x
		}
		return points[a].y < points[b].y
	})
	cross := func(o, a, b pt) float64 { return (a.x-o.x)*(b.y-o.y) - (a.y-o.y)*(b.x-o.x) }
	hull := make([]pt, 0, 2*len(points))
	for _, p := range points {
		for len(hull) >= 2 && cross(hull[len(hull)-2], hull[len(hull)-1], p) <= 0 {
			hull = hull[:len(hull)-1]
		}
		hull = append(hull, p)
	}
	lower := len(hull) + 1
	for i := len(points) - 2; i >= 0; i-- {
		p := points[i]
		for len(hull) >= lower && cross(hull[len(hull)-2], hull[len(hull)-1], p) <= 0 {
			hull = hull[:len(hull)-1]
		}
		hull = append(hull, p)
	}
	return hull[:len(hull)-1]
}

// minAreaRect finds the minimum-area enclosing rectangle of a convex hull by
// rotating calipers, returned as an ordered quad, plus its shorter side.
// Pixel centres are widened by half a pixel so a one-pixel-wide run has
// width 1, as with contour-based rectangles.
func minAreaRect(hull []pt) (quad, float64) {
	best := math.Inf(1)
	var bestQuad quad
	var bestShort float64
	n := len(hull)
	for i := range n {
		a, b := hull[i], hull[(i+1)%n]
		ex, ey := b.x-a.x, b.y-a.y
		l := math.Hypot(ex, ey)
		if l == 0 {
			continue
		}
		ux, uy := ex/l, ey/l
		vx, vy := -uy, ux
		minU, maxU, minV, maxV := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
		for _, p := range hull {
			u := p.x*ux + p.y*uy
			v := p.x*vx + p.y*vy
			minU, maxU = math.Min(minU, u), math.Max(maxU, u)
			minV, maxV = math.Min(minV, v), math.Max(maxV, v)
		}
		area := (maxU - minU) * (maxV - minV)
		if area < best {
			best = area
			corner := func(u, v float64) pt { return pt{u*ux + v*vx, u*uy + v*vy} }
			bestQuad = orderQuad([4]pt{corner(minU, minV), corner(maxU, minV), corner(maxU, maxV), corner(minU, maxV)})
			bestShort = math.Min(maxU-minU, maxV-minV)
		}
	}
	if n == 1 || math.IsInf(best, 1) {
		p := hull[0]
		return quad{p, p, p, p}, 0
	}
	// Widen by half a pixel on every side.
	return grow(bestQuad, 0.5), bestShort + 1
}

// orderQuad orders rectangle corners as PaddleOCR's get_mini_boxes does:
// the two leftmost points give top-left and bottom-left, the two rightmost
// top-right and bottom-right.
func orderQuad(c [4]pt) quad {
	sort.Slice(c[:], func(a, b int) bool { return c[a].x < c[b].x })
	tl, bl := c[0], c[1]
	if bl.y < tl.y {
		tl, bl = bl, tl
	}
	tr, br := c[2], c[3]
	if br.y < tr.y {
		tr, br = br, tr
	}
	return quad{tl, tr, br, bl}
}

// grow moves every edge of rectangle q outward by d.
func grow(q quad, d float64) quad {
	cx := (q[0].x + q[1].x + q[2].x + q[3].x) / 4
	cy := (q[0].y + q[1].y + q[2].y + q[3].y) / 4
	w, h := dist(q[0], q[1]), dist(q[0], q[3])
	if w == 0 || h == 0 {
		return q
	}
	ux, uy := (q[1].x-q[0].x)/w, (q[1].y-q[0].y)/w
	vx, vy := (q[3].x-q[0].x)/h, (q[3].y-q[0].y)/h
	hw, hh := w/2+d, h/2+d
	corner := func(su, sv float64) pt {
		return pt{cx + su*hw*ux + sv*hh*vx, cy + su*hw*uy + sv*hh*vy}
	}
	return quad{corner(-1, -1), corner(1, -1), corner(1, 1), corner(-1, 1)}
}

// unclip expands a rectangle the way DB's polygon offset does: by
// area·ratio/perimeter on every side.
func unclip(q quad, ratio float64) (quad, float64) {
	w, h := dist(q[0], q[1]), dist(q[0], q[3])
	if w+h == 0 {
		return q, 0
	}
	d := w * h * ratio / (2 * (w + h))
	return grow(q, d), math.Min(w, h) + 2*d
}

// boxScore is the mean probability inside q.
func boxScore(prob []float32, stride, vw, vh int, q quad) float64 {
	x0, y0, x1, y1 := q.bounds()
	ix0, iy0 := max(0, int(math.Floor(x0))), max(0, int(math.Floor(y0)))
	ix1, iy1 := min(vw-1, int(math.Ceil(x1))), min(vh-1, int(math.Ceil(y1)))
	sum, n := 0.0, 0
	for y := iy0; y <= iy1; y++ {
		for x := ix0; x <= ix1; x++ {
			if inside(q, float64(x), float64(y)) {
				sum += float64(prob[y*stride+x])
				n++
			}
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// inside reports whether (x, y) lies in convex quad q.
func inside(q quad, x, y float64) bool {
	sign := 0.0
	for i := range 4 {
		a, b := q[i], q[(i+1)%4]
		c := (b.x-a.x)*(y-a.y) - (b.y-a.y)*(x-a.x)
		if c == 0 {
			continue
		}
		if sign == 0 {
			sign = c
		} else if (c > 0) != (sign > 0) {
			return false
		}
	}
	return true
}

// sortBoxes orders boxes top to bottom, then left to right within a line
// (PaddleOCR's sorted_boxes).
func sortBoxes(boxes []quad) {
	sort.SliceStable(boxes, func(a, b int) bool {
		if boxes[a][0].y != boxes[b][0].y {
			return boxes[a][0].y < boxes[b][0].y
		}
		return boxes[a][0].x < boxes[b][0].x
	})
	for i := 0; i < len(boxes)-1; i++ {
		for j := i; j >= 0; j-- {
			if math.Abs(boxes[j+1][0].y-boxes[j][0].y) < 10 && boxes[j+1][0].x < boxes[j][0].x {
				boxes[j], boxes[j+1] = boxes[j+1], boxes[j]
			} else {
				break
			}
		}
	}
}
