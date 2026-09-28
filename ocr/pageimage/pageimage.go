// Package pageimage extracts the image of a scanned PDF page in pure Go, for
// OCR engines and adapter authors.
//
// A scanned page is usually one image XObject painted over the page. Extract
// finds the largest image the page paints (directly or through form
// XObjects), decodes it (JPEG passthrough, or Flate/uncompressed samples in
// DeviceGray, DeviceRGB, DeviceCMYK, ICCBased or Indexed colour) and reports
// where it sits on the page, so recognized boxes can be mapped back to page
// coordinates.
//
// Images in other encodings (CCITT, JBIG2, JPEG 2000) and pages without an
// image return ErrNoImage; callers can fall back to a renderer such as
// pdftoppm (see Render).
package pageimage

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"

	"github.com/m7medVision/docstomd-go/internal/pdf/parse"
)

// ErrNoImage means the page paints no image this package can decode.
var ErrNoImage = errors.New("pageimage: no decodable page image")

// Limits keep hostile files from exhausting memory.
const (
	// maxPixels bounds one decoded image (about 100 megapixels).
	maxPixels = 100 << 20
	// maxOps bounds content-stream operators interpreted per page.
	maxOps = 1 << 20
	// maxFormDepth bounds form XObject nesting.
	maxFormDepth = 8
)

// Page is a decoded page image and its placement.
type Page struct {
	// Image is upright in page space: its top row is the top of the page.
	Image image.Image
	// Width and Height are the visible page box (CropBox ∩ MediaBox) in
	// points.
	Width, Height float64
	// Placement is where Image is painted, in points from the top-left of
	// the visible page, y down.
	Placement Rect
}

// Rect is an axis-aligned rectangle.
type Rect struct{ X0, Y0, X1, Y1 float64 }

// ToPage maps a point in image pixels to page points (top-left origin).
func (p *Page) ToPage(x, y float64) (float64, float64) {
	b := p.Image.Bounds()
	sx := (p.Placement.X1 - p.Placement.X0) / float64(b.Dx())
	sy := (p.Placement.Y1 - p.Placement.Y0) / float64(b.Dy())
	return p.Placement.X0 + x*sx, p.Placement.Y0 + y*sy
}

// Document is a parsed PDF whose pages can be extracted.
type Document struct {
	doc *parse.Document
}

// Open parses a PDF. Encrypted documents are not supported.
func Open(pdf []byte) (*Document, error) {
	doc, err := parse.Parse(pdf)
	if err != nil {
		return nil, err
	}
	return &Document{doc: doc}, nil
}

// PageCount is the number of pages.
func (d *Document) PageCount() int { return len(d.doc.Pages()) }

// Extract decodes the image of 1-indexed page n.
func (d *Document) Extract(n int) (*Page, error) {
	pages := d.doc.Pages()
	if n < 1 || n > len(pages) {
		return nil, fmt.Errorf("pageimage: page %d out of range 1..%d", n, len(pages))
	}
	ref := pages[n-1]
	pageDict := d.doc.PageDict(ref)
	if pageDict == nil {
		return nil, ErrNoImage
	}
	box := d.pageBox(pageDict)
	s := &scanner{doc: d.doc}
	for _, contentRef := range d.doc.PageContents(ref) {
		obj, err := d.doc.GetObject(contentRef.Num)
		if err != nil {
			continue
		}
		data, ok := d.doc.StreamData(obj)
		if !ok {
			continue
		}
		s.run(data, d.doc.PageResources(ref), identity, 0)
	}
	if s.best == nil {
		return nil, ErrNoImage
	}
	img, err := d.decode(s.best.stream)
	if err != nil {
		return nil, err
	}
	img = orient(img, s.best.ctm)
	x0, y0, x1, y1 := s.best.ctm.bbox()
	placement := Rect{
		X0: x0 - box.x0, X1: x1 - box.x0,
		Y0: box.y1 - y1, Y1: box.y1 - y0,
	}
	return &Page{Image: img, Width: box.x1 - box.x0, Height: box.y1 - box.y0, Placement: placement}, nil
}

type pageBox struct{ x0, y0, x1, y1 float64 }

// pageBox mirrors the core extractor: CropBox ∩ MediaBox, else either, else
// US Letter.
func (d *Document) pageBox(pageDict map[string]any) pageBox {
	media := d.boxOf(pageDict["MediaBox"])
	crop := d.boxOf(pageDict["CropBox"])
	switch {
	case media != nil && crop != nil:
		b := pageBox{math.Max(media.x0, crop.x0), math.Max(media.y0, crop.y0), math.Min(media.x1, crop.x1), math.Min(media.y1, crop.y1)}
		if b.x1 > b.x0 && b.y1 > b.y0 {
			return b
		}
		return *media
	case crop != nil:
		return *crop
	case media != nil:
		return *media
	}
	return pageBox{0, 0, 612, 792}
}

func (d *Document) boxOf(obj any) *pageBox {
	arr, ok := d.doc.Resolve(obj).([]any)
	if !ok || len(arr) < 4 {
		return nil
	}
	var v [4]float64
	for i := range v {
		f, ok := number(d.doc.Resolve(arr[i]))
		if !ok {
			return nil
		}
		v[i] = f
	}
	b := pageBox{math.Min(v[0], v[2]), math.Min(v[1], v[3]), math.Max(v[0], v[2]), math.Max(v[1], v[3])}
	if b.x1 <= b.x0 || b.y1 <= b.y0 {
		return nil
	}
	return &b
}

func number(obj any) (float64, bool) {
	switch v := obj.(type) {
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, false
		}
		return v, true
	}
	return 0, false
}

// matrix is a PDF transformation [a b c d e f].
type matrix [6]float64

var identity = matrix{1, 0, 0, 1, 0, 0}

// mul returns m × n (m applied first).
func (m matrix) mul(n matrix) matrix {
	return matrix{
		m[0]*n[0] + m[1]*n[2], m[0]*n[1] + m[1]*n[3],
		m[2]*n[0] + m[3]*n[2], m[2]*n[1] + m[3]*n[3],
		m[4]*n[0] + m[5]*n[2] + n[4], m[4]*n[1] + m[5]*n[3] + n[5],
	}
}

func (m matrix) apply(x, y float64) (float64, float64) {
	return m[0]*x + m[2]*y + m[4], m[1]*x + m[3]*y + m[5]
}

// bbox is the page-space bounding box of the unit square under m.
func (m matrix) bbox() (x0, y0, x1, y1 float64) {
	x0, y0 = math.Inf(1), math.Inf(1)
	x1, y1 = math.Inf(-1), math.Inf(-1)
	for _, p := range [][2]float64{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
		x, y := m.apply(p[0], p[1])
		x0, y0, x1, y1 = math.Min(x0, x), math.Min(y0, y), math.Max(x1, x), math.Max(y1, y)
	}
	return
}

func (m matrix) area() float64 {
	return math.Abs(m[0]*m[3] - m[1]*m[2])
}

type placed struct {
	stream *parse.Stream
	ctm    matrix
}

// scanner walks content streams tracking the CTM and records the image
// covering the largest page area.
type scanner struct {
	doc  *parse.Document
	best *placed
	ops  int
}

func (s *scanner) run(content []byte, resources []map[string]any, ctm matrix, depth int) {
	var stack []matrix
	ops := &opScanner{data: content}
	for {
		op, ok := ops.next()
		if !ok || s.ops > maxOps {
			return
		}
		s.ops++
		switch op.operator {
		case "q":
			if len(stack) < maxStack {
				stack = append(stack, ctm)
			}
		case "Q":
			if n := len(stack); n > 0 {
				ctm, stack = stack[n-1], stack[:n-1]
			}
		case "cm":
			if len(op.operands) == 6 {
				var m matrix
				valid := true
				for i := range m {
					f, ok := number(op.operands[i])
					valid = valid && ok
					m[i] = f
				}
				if valid {
					ctm = m.mul(ctm)
				}
			}
		case "Do":
			if len(op.operands) == 1 {
				if name, ok := op.operands[0].(parse.Name); ok {
					s.xobject(string(name), resources, ctm, depth)
				}
			}
		}
	}
}

func (s *scanner) xobject(name string, resources []map[string]any, ctm matrix, depth int) {
	var obj any
	for _, res := range resources {
		xobjects, ok := s.doc.Resolve(res["XObject"]).(map[string]any)
		if !ok {
			continue
		}
		if v, ok := xobjects[name]; ok {
			obj = s.doc.Resolve(v)
			break
		}
	}
	stm, ok := obj.(*parse.Stream)
	if !ok {
		return
	}
	switch stm.Dict["Subtype"] {
	case parse.Name("Image"):
		if s.best == nil || ctm.area() > s.best.ctm.area() {
			s.best = &placed{stream: stm, ctm: ctm}
		}
	case parse.Name("Form"):
		if depth >= maxFormDepth {
			return
		}
		formCTM := ctm
		if arr, ok := s.doc.Resolve(stm.Dict["Matrix"]).([]any); ok && len(arr) == 6 {
			var m matrix
			valid := true
			for i := range m {
				f, ok := number(s.doc.Resolve(arr[i]))
				valid = valid && ok
				m[i] = f
			}
			if valid {
				formCTM = m.mul(ctm)
			}
		}
		inner := resources
		if res, ok := s.doc.Resolve(stm.Dict["Resources"]).(map[string]any); ok {
			inner = append([]map[string]any{res}, resources...)
		}
		if data, ok := s.doc.StreamData(stm); ok {
			s.run(data, inner, formCTM, depth+1)
		}
	}
}

// decode turns an image XObject into an image.Image in image space (row 0
// is the top of the image as stored).
func (d *Document) decode(stm *parse.Stream) (image.Image, error) {
	dict := stm.Dict
	width, _ := number(d.doc.Resolve(dict["Width"]))
	height, _ := number(d.doc.Resolve(dict["Height"]))
	w, h := int(width), int(height)
	if w <= 0 || h <= 0 || w*h > maxPixels {
		return nil, fmt.Errorf("%w: image size %dx%d", ErrNoImage, w, h)
	}
	filters := filterNames(d.doc.Resolve(dict["Filter"]))
	if len(filters) == 1 && filters[0] == "DCTDecode" {
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(stm.Raw))
		if err != nil || cfg.Width*cfg.Height > maxPixels {
			return nil, fmt.Errorf("%w: bad JPEG", ErrNoImage)
		}
		img, err := jpeg.Decode(bytes.NewReader(stm.Raw))
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrNoImage, err)
		}
		if cmyk, ok := img.(*image.CMYK); ok && adobeInverted(stm.Raw) {
			invertCMYK(cmyk)
		}
		return img, nil
	}
	for _, f := range filters {
		if f != "FlateDecode" && f != "ASCIIHexDecode" && f != "ASCII85Decode" {
			return nil, fmt.Errorf("%w: %s images are not decoded in pure Go", ErrNoImage, f)
		}
	}
	if mask, _ := d.doc.Resolve(dict["ImageMask"]).(bool); mask {
		return nil, fmt.Errorf("%w: image mask", ErrNoImage)
	}
	data, ok := d.doc.StreamData(stm)
	if !ok {
		return nil, fmt.Errorf("%w: undecodable image stream", ErrNoImage)
	}
	bpcF, _ := number(d.doc.Resolve(dict["BitsPerComponent"]))
	bpc := int(bpcF)
	space, err := d.colorSpace(dict["ColorSpace"])
	if err != nil {
		return nil, err
	}
	return samples(data, w, h, bpc, space)
}

func filterNames(obj any) []string {
	switch f := obj.(type) {
	case parse.Name:
		return []string{string(f)}
	case []any:
		var out []string
		for _, v := range f {
			if n, ok := v.(parse.Name); ok {
				out = append(out, string(n))
			}
		}
		return out
	}
	return nil
}

// colorSpace describes how samples map to colour.
type colorSpace struct {
	components int
	// palette is set for Indexed spaces: base-space RGB per index.
	palette []color.RGBA
	cmyk    bool
}

func (d *Document) colorSpace(obj any) (colorSpace, error) {
	obj = d.doc.Resolve(obj)
	switch v := obj.(type) {
	case parse.Name:
		switch v {
		case "DeviceGray", "CalGray", "G":
			return colorSpace{components: 1}, nil
		case "DeviceRGB", "CalRGB", "RGB":
			return colorSpace{components: 3}, nil
		case "DeviceCMYK", "CMYK":
			return colorSpace{components: 4, cmyk: true}, nil
		}
	case []any:
		if len(v) == 0 {
			break
		}
		family, _ := d.doc.Resolve(v[0]).(parse.Name)
		switch family {
		case "ICCBased":
			if len(v) > 1 {
				if stm, ok := d.doc.Resolve(v[1]).(*parse.Stream); ok {
					n, _ := number(d.doc.Resolve(stm.Dict["N"]))
					switch int(n) {
					case 1:
						return colorSpace{components: 1}, nil
					case 3:
						return colorSpace{components: 3}, nil
					case 4:
						return colorSpace{components: 4, cmyk: true}, nil
					}
				}
			}
		case "CalGray":
			return colorSpace{components: 1}, nil
		case "CalRGB":
			return colorSpace{components: 3}, nil
		case "Indexed", "I":
			if len(v) < 4 {
				break
			}
			base, err := d.colorSpace(v[1])
			if err != nil || base.palette != nil {
				break
			}
			hival, _ := number(d.doc.Resolve(v[2]))
			var lookup []byte
			switch t := d.doc.Resolve(v[3]).(type) {
			case []byte:
				lookup = t
			case string:
				lookup = []byte(t)
			case *parse.Stream:
				lookup, _ = d.doc.StreamData(t)
			}
			n := int(hival) + 1
			if n < 1 || n > 256 || len(lookup) < n*base.components {
				break
			}
			palette := make([]color.RGBA, n)
			for i := range palette {
				palette[i] = toRGBA(lookup[i*base.components:(i+1)*base.components], base)
			}
			return colorSpace{components: 1, palette: palette}, nil
		}
	}
	return colorSpace{}, fmt.Errorf("%w: unsupported colour space %v", ErrNoImage, obj)
}

func toRGBA(c []byte, space colorSpace) color.RGBA {
	switch {
	case space.components == 1:
		return color.RGBA{c[0], c[0], c[0], 255}
	case space.cmyk:
		r, g, b := color.CMYKToRGB(c[0], c[1], c[2], c[3])
		return color.RGBA{r, g, b, 255}
	}
	return color.RGBA{c[0], c[1], c[2], 255}
}

// samples unpacks raw image samples.
func samples(data []byte, w, h, bpc int, space colorSpace) (image.Image, error) {
	if bpc != 1 && bpc != 2 && bpc != 4 && bpc != 8 {
		return nil, fmt.Errorf("%w: %d bits per component", ErrNoImage, bpc)
	}
	rowBytes := (w*space.components*bpc + 7) / 8
	if len(data) < rowBytes*h {
		return nil, fmt.Errorf("%w: image data too short", ErrNoImage)
	}
	maxVal := (1 << bpc) - 1
	sample := func(row []byte, i int) int {
		switch bpc {
		case 8:
			return int(row[i])
		default:
			bit := i * bpc
			return int(row[bit/8]>>(8-bpc-bit%8)) & maxVal
		}
	}
	scale := func(v int) uint8 { return uint8(v * 255 / maxVal) }
	switch {
	case space.palette != nil:
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := range h {
			row := data[y*rowBytes:]
			for x := range w {
				idx := sample(row, x)
				if idx < len(space.palette) {
					img.SetRGBA(x, y, space.palette[idx])
				}
			}
		}
		return img, nil
	case space.components == 1:
		img := image.NewGray(image.Rect(0, 0, w, h))
		for y := range h {
			row := data[y*rowBytes:]
			for x := range w {
				img.Pix[y*img.Stride+x] = scale(sample(row, x))
			}
		}
		return img, nil
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	c := make([]byte, space.components)
	for y := range h {
		row := data[y*rowBytes:]
		for x := range w {
			for k := range c {
				c[k] = scale(sample(row, x*space.components+k))
			}
			img.SetRGBA(x, y, toRGBA(c, space))
		}
	}
	return img, nil
}

// adobeInverted reports whether a CMYK JPEG carries an Adobe APP14 marker,
// whose CMYK samples are stored inverted.
func adobeInverted(data []byte) bool {
	return bytes.Contains(data[:min(len(data), 64<<10)], []byte("Adobe"))
}

func invertCMYK(img *image.CMYK) {
	for i := range img.Pix {
		img.Pix[i] = 255 - img.Pix[i]
	}
}

// orient rotates or flips img so that it is upright in page space. An image
// is painted into the unit square under ctm, with its first row at y=1.
func orient(img image.Image, ctm matrix) image.Image {
	const eps = 1e-9
	a, b, c, d := ctm[0], ctm[1], ctm[2], ctm[3]
	switch {
	case math.Abs(b) < eps && math.Abs(c) < eps:
		// Axis aligned: image x → page x (sign a), image rows go top → bottom
		// in page space when d > 0.
		return transform(img, a < 0, d < 0, false)
	case math.Abs(a) < eps && math.Abs(d) < eps:
		// Rotated by ±90°: image x → page y.
		return transform(img, c > 0, b > 0, true)
	}
	return img
}

// transform returns img optionally transposed, then mirrored horizontally
// and/or vertically, as an RGBA or Gray image.
func transform(img image.Image, flipX, flipY, transpose bool) image.Image {
	if !flipX && !flipY && !transpose {
		return img
	}
	src := img.Bounds()
	w, h := src.Dx(), src.Dy()
	if transpose {
		w, h = h, w
	}
	gray, isGray := img.(*image.Gray)
	var out draw
	if isGray {
		out = image.NewGray(image.Rect(0, 0, w, h))
	} else {
		out = image.NewRGBA(image.Rect(0, 0, w, h))
	}
	for y := range h {
		for x := range w {
			sx, sy := x, y
			if flipX {
				sx = w - 1 - x
			}
			if flipY {
				sy = h - 1 - y
			}
			if transpose {
				sx, sy = sy, sx
			}
			if isGray {
				out.(*image.Gray).Pix[y*w+x] = gray.GrayAt(src.Min.X+sx, src.Min.Y+sy).Y
			} else {
				out.Set(x, y, img.At(src.Min.X+sx, src.Min.Y+sy))
			}
		}
	}
	return out
}

type draw interface {
	image.Image
	Set(x, y int, c color.Color)
}
