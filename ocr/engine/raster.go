package engine

import (
	"image"
	"math"
)

// raster is an 8-bit RGB image, row-major.
type raster struct {
	w, h int
	pix  []uint8
}

func newRaster(w, h int) *raster {
	return &raster{w: w, h: h, pix: make([]uint8, w*h*3)}
}

// toRaster converts any image to RGB.
func toRaster(img image.Image) *raster {
	b := img.Bounds()
	r := newRaster(b.Dx(), b.Dy())
	switch src := img.(type) {
	case *image.Gray:
		for y := range r.h {
			row := src.Pix[(y+b.Min.Y-src.Rect.Min.Y)*src.Stride+(b.Min.X-src.Rect.Min.X):]
			for x := range r.w {
				v := row[x]
				o := (y*r.w + x) * 3
				r.pix[o], r.pix[o+1], r.pix[o+2] = v, v, v
			}
		}
	case *image.RGBA:
		for y := range r.h {
			row := src.Pix[(y+b.Min.Y-src.Rect.Min.Y)*src.Stride+(b.Min.X-src.Rect.Min.X)*4:]
			for x := range r.w {
				o := (y*r.w + x) * 3
				copy(r.pix[o:o+3], row[x*4:x*4+3])
			}
		}
	default:
		for y := range r.h {
			for x := range r.w {
				cr, cg, cb, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
				o := (y*r.w + x) * 3
				r.pix[o], r.pix[o+1], r.pix[o+2] = uint8(cr>>8), uint8(cg>>8), uint8(cb>>8)
			}
		}
	}
	return r
}

// at samples channel c at (x, y) bilinearly, clamping to the edges.
func (r *raster) at(x, y float64, c int) float64 {
	x = math.Max(0, math.Min(x, float64(r.w-1)))
	y = math.Max(0, math.Min(y, float64(r.h-1)))
	x0, y0 := int(x), int(y)
	x1, y1 := min(x0+1, r.w-1), min(y0+1, r.h-1)
	fx, fy := x-float64(x0), y-float64(y0)
	p := func(px, py int) float64 { return float64(r.pix[(py*r.w+px)*3+c]) }
	top := p(x0, y0)*(1-fx) + p(x1, y0)*fx
	bot := p(x0, y1)*(1-fx) + p(x1, y1)*fx
	return top*(1-fy) + bot*fy
}

// resize scales r to w×h with bilinear sampling (pixel-centre aligned).
func (r *raster) resize(w, h int) *raster {
	if w == r.w && h == r.h {
		return r
	}
	out := newRaster(w, h)
	sx, sy := float64(r.w)/float64(w), float64(r.h)/float64(h)
	for y := range h {
		fy := (float64(y)+0.5)*sy - 0.5
		for x := range w {
			fx := (float64(x)+0.5)*sx - 0.5
			o := (y*w + x) * 3
			for c := range 3 {
				out.pix[o+c] = uint8(math.Round(r.at(fx, fy, c)))
			}
		}
	}
	return out
}

// rotate180 turns r upside down.
func (r *raster) rotate180() *raster {
	out := newRaster(r.w, r.h)
	n := r.w * r.h
	for i := range n {
		copy(out.pix[(n-1-i)*3:(n-i)*3], r.pix[i*3:i*3+3])
	}
	return out
}

// crop samples the rotated rectangle q (corners clockwise from top-left)
// into an upright w×h raster.
func (r *raster) crop(q quad, w, h int) *raster {
	out := newRaster(w, h)
	// Unit vectors along the top edge and the left edge.
	ux, uy := (q[1].x-q[0].x)/float64(w), (q[1].y-q[0].y)/float64(w)
	vx, vy := (q[3].x-q[0].x)/float64(h), (q[3].y-q[0].y)/float64(h)
	for y := range h {
		for x := range w {
			fx := q[0].x + (float64(x)+0.5)*ux + (float64(y)+0.5)*vx - 0.5
			fy := q[0].y + (float64(x)+0.5)*uy + (float64(y)+0.5)*vy - 0.5
			o := (y*w + x) * 3
			for c := range 3 {
				out.pix[o+c] = uint8(math.Round(r.at(fx, fy, c)))
			}
		}
	}
	return out
}

// transpose turns r 90° counter-clockwise, for vertical text crops.
func (r *raster) rotate90ccw() *raster {
	out := newRaster(r.h, r.w)
	for y := range r.h {
		for x := range r.w {
			// (x, y) → (y, w-1-x)
			o := ((r.w-1-x)*out.w + y) * 3
			copy(out.pix[o:o+3], r.pix[(y*r.w+x)*3:(y*r.w+x)*3+3])
		}
	}
	return out
}

// normalize writes r into dst as CHW float32 at offset (0, 0) of a
// dstW×dstH plane per channel, applying the input's channel order, scale,
// mean and std.
func (in Input) normalize(dst []float32, r *raster, dstW, dstH int) {
	in.normalizeAt(dst, r, dstW, dstH, 0, 0)
}

// normalizeAt is normalize with r placed at (ox, oy).
func (in Input) normalizeAt(dst []float32, r *raster, dstW, dstH, ox, oy int) {
	order := [3]int{2, 1, 0} // BGR
	if in.Color == "rgb" {
		order = [3]int{0, 1, 2}
	}
	plane := dstW * dstH
	for c := range 3 {
		src := order[c]
		scale := in.Scale / in.Std[c]
		shift := in.Mean[c] / in.Std[c]
		for y := range min(r.h, dstH-oy) {
			row := dst[c*plane+(y+oy)*dstW+ox:]
			for x := range min(r.w, dstW-ox) {
				row[x] = float32(float64(r.pix[(y*r.w+x)*3+src])*scale - shift)
			}
		}
	}
}

// fill sets a CHW tensor region to the normalized value of the gray level v.
func (in Input) fill(dst []float32, v uint8, dstW, dstH int) {
	plane := dstW * dstH
	for c := range 3 {
		val := float32(float64(v)*in.Scale/in.Std[c] - in.Mean[c]/in.Std[c])
		for i := range plane {
			dst[c*plane+i] = val
		}
	}
}
