package engine

import (
	"errors"
	"fmt"
	"image"
	"math"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gomlx/compute"
)

// Line is one recognized text line; Box is in image pixels (top-left
// origin, y down).
type Line struct {
	Text       string
	X0, Y0     float64
	X1, Y1     float64
	Confidence float64
}

// Engine runs one model. It is not safe for concurrent use.
type Engine struct {
	Manifest *Manifest
	det      *net
	cls      *net
	rec      *net
	chars    []string
	classes  int
}

// defaultDropScore drops recognized lines below this confidence.
const defaultDropScore = 0.5

// Load reads the manifest in dir, its character list and its models, and
// prepares them for backend.
func Load(dir string, backend compute.Backend) (*Engine, error) {
	m, err := LoadManifest(dir)
	if err != nil {
		return nil, err
	}
	chars, err := loadCharset(dir, m.Recognizer.Charset)
	if err != nil {
		return nil, err
	}
	e := &Engine{Manifest: m, chars: chars}
	e.classes = len(chars) + 1
	if m.Recognizer.Decoder.AppendSpace {
		e.chars = append(e.chars, " ")
		e.classes++
	}
	if e.det, err = loadNet(backend, filepath.Join(dir, m.Detector.Model)); err != nil {
		return nil, err
	}
	if m.Classifier != nil {
		if e.cls, err = loadNet(backend, filepath.Join(dir, m.Classifier.Model)); err != nil {
			e.Close()
			return nil, err
		}
	}
	if e.rec, err = loadNet(backend, filepath.Join(dir, m.Recognizer.Model)); err != nil {
		e.Close()
		return nil, err
	}
	return e, nil
}

// Close releases the compiled models.
func (e *Engine) Close() {
	for _, n := range []*net{e.det, e.cls, e.rec} {
		if n != nil {
			n.close()
		}
	}
}

// Denormals counts subnormal weights left in the loaded models (0 after the
// load-time flush).
func (e *Engine) Denormals() int {
	count := 0
	for _, n := range []*net{e.det, e.cls, e.rec} {
		if n != nil {
			count += n.denormals()
		}
	}
	return count
}

// FellBack lists the models that moved to the pure-Go backend because the
// selected backend cannot build their graphs.
func (e *Engine) FellBack() []string {
	var names []string
	for _, n := range []*net{e.det, e.cls, e.rec} {
		if n != nil && n.fellBack {
			names = append(names, n.name)
		}
	}
	return names
}

// Flushed counts the subnormal weights zeroed at load.
func (e *Engine) Flushed() int {
	count := 0
	for _, n := range []*net{e.det, e.cls, e.rec} {
		if n != nil {
			count += n.flushed
		}
	}
	return count
}

// Builds counts graph compilations so far, one per model and input shape.
func (e *Engine) Builds() int64 {
	var count int64
	for _, n := range []*net{e.det, e.cls, e.rec} {
		if n != nil {
			count += n.builds.Load()
		}
	}
	return count
}

// Recognize finds and reads the text lines of img, in reading order.
func (e *Engine) Recognize(img image.Image) ([]Line, error) {
	r := toRaster(img)
	if r.w < 4 || r.h < 4 {
		return nil, nil
	}
	boxes, err := e.detect(r)
	if err != nil {
		return nil, err
	}
	if len(boxes) == 0 {
		return nil, nil
	}
	crops := make([]*raster, len(boxes))
	for i, q := range boxes {
		w, h := q.size()
		cw, ch := max(1, int(math.Round(w))), max(1, int(math.Round(h)))
		crop := r.crop(q, cw, ch)
		if float64(ch)/float64(cw) >= 1.5 {
			crop = crop.rotate90ccw()
		}
		crops[i] = crop
	}
	if e.cls != nil {
		if err := e.orient(crops); err != nil {
			return nil, err
		}
	}
	texts, scores, err := e.read(crops)
	if err != nil {
		return nil, err
	}
	var lines []Line
	for i, q := range boxes {
		text := texts[i]
		if e.Manifest.OutputOrder == "visual" {
			text = visualToLogical(text)
		}
		text = strings.TrimSpace(normalize(text))
		if text == "" || scores[i] < defaultDropScore {
			continue
		}
		x0, y0, x1, y1 := q.bounds()
		lines = append(lines, Line{Text: text, X0: x0, Y0: y0, X1: x1, Y1: y1, Confidence: scores[i]})
	}
	return lines, nil
}

// detect runs the detector and returns text boxes in image pixels.
func (e *Engine) detect(r *raster) ([]quad, error) {
	d := e.Manifest.Detector
	multiple := d.Multiple
	if multiple == 0 {
		multiple = 32
	}
	ratio := 1.0
	switch d.LimitType {
	case "fit":
		ratio = math.Min(float64(d.LimitSide)/float64(r.w), float64(d.LimitSide)/float64(r.h))
	case "min":
		if s := min(r.w, r.h); s < d.LimitSide {
			ratio = float64(d.LimitSide) / float64(s)
		}
	default:
		if s := max(r.w, r.h); s > d.LimitSide {
			ratio = float64(d.LimitSide) / float64(s)
		}
	}
	round := math.Round
	if d.LimitType == "fit" {
		round = math.Floor // stay inside the square
	}
	rw := max(multiple, int(round(float64(r.w)*ratio/float64(multiple)))*multiple)
	rh := max(multiple, int(round(float64(r.h)*ratio/float64(multiple)))*multiple)
	bw, bh := rw, rh
	best := math.MaxInt
	for _, b := range d.Buckets {
		if b[0] >= rh && b[1] >= rw && b[0]*b[1] < best {
			bh, bw, best = b[0], b[1], b[0]*b[1]
		}
	}
	padValue := uint8(255)
	if d.PadValue != nil {
		padValue = uint8(*d.PadValue)
	}
	ox, oy := 0, 0
	if d.Pad == "center" {
		ox, oy = (bw-rw+1)/2, (bh-rh+1)/2
	}
	input := make([]float32, 3*bw*bh)
	d.Input.fill(input, padValue, bw, bh)
	d.Input.normalizeAt(input, r.resize(rw, rh), bw, bh, ox, oy)
	prob, dims, err := e.det.run(input, 1, 3, bh, bw)
	if err != nil {
		return nil, err
	}
	if len(dims) != 4 || dims[2] != bh || dims[3] != bw {
		return nil, fmt.Errorf("detector output %v, want [1 1 %d %d]", dims, bh, bw)
	}
	// The image region of the map, activated.
	valid := make([]float32, rw*rh)
	for y := range rh {
		copy(valid[y*rw:(y+1)*rw], prob[(y+oy)*bw+ox:(y+oy)*bw+ox+rw])
	}
	if d.PostProcess.Activation == "sigmoid" {
		for i, v := range valid {
			valid[i] = float32(1 / (1 + math.Exp(-float64(v))))
		}
	}
	return dbBoxes(valid, rw, rw, rh, d.PostProcess, float64(r.w)/float64(rw), float64(r.h)/float64(rh), r.w, r.h), nil
}

// orient turns upside-down crops upright.
func (e *Engine) orient(crops []*raster) error {
	c := e.Manifest.Classifier
	for start := 0; start < len(crops); {
		n := batchSize(c.Batches, len(crops)-start)
		plane := 3 * c.Width * c.Height
		input := make([]float32, n*plane)
		for i := range n {
			c.Input.normalize(input[i*plane:], crops[start+i].resize(c.Width, c.Height), c.Width, c.Height)
		}
		out, dims, err := e.cls.run(input, n, 3, c.Height, c.Width)
		if err != nil {
			return err
		}
		if len(dims) != 2 || dims[1] != len(c.Angles) {
			return fmt.Errorf("classifier output %v, want [%d %d]", dims, n, len(c.Angles))
		}
		for i := range n {
			row := out[i*dims[1] : (i+1)*dims[1]]
			best := 0
			for k := range row {
				if row[k] > row[best] {
					best = k
				}
			}
			if c.Angles[best] == 180 && float64(row[best]) >= c.Threshold {
				crops[start+i] = crops[start+i].rotate180()
			}
		}
		start += n
	}
	return nil
}

// batchSize is the largest bucket no bigger than remaining, or 1.
func batchSize(buckets []int, remaining int) int {
	best := 1
	for _, b := range buckets {
		if b <= remaining && b > best {
			best = b
		}
	}
	return best
}

// read recognizes crops in batches of similar aspect ratio.
func (e *Engine) read(crops []*raster) ([]string, []float64, error) {
	rc := e.Manifest.Recognizer
	h := rc.Height
	order := make([]int, len(crops))
	for i := range order {
		order[i] = i
	}
	ratio := func(i int) float64 { return float64(crops[i].w) / float64(crops[i].h) }
	sort.SliceStable(order, func(a, b int) bool { return ratio(order[a]) < ratio(order[b]) })
	texts := make([]string, len(crops))
	scores := make([]float64, len(crops))
	for start := 0; start < len(order); {
		n := batchSize(rc.Batches, len(order)-start)
		idx := order[start : start+n]
		start += n
		maxRatio := 0.0
		for _, i := range idx {
			maxRatio = math.Max(maxRatio, ratio(i))
		}
		width := rc.Width
		if width == 0 {
			width = e.recWidth(int(math.Ceil(float64(h) * maxRatio)))
		}
		plane := 3 * width * h
		input := make([]float32, n*plane)
		for k, i := range idx {
			w, ch := min(width, max(1, int(math.Ceil(float64(h)*ratio(i))))), h
			if rc.Width > 0 && ratio(i) > float64(width)/float64(h) {
				// Wider than the fixed input: fit the width, pad below.
				w, ch = width, max(1, int(float64(width)/ratio(i)))
			}
			if rc.PadValue != nil {
				rc.Input.fill(input[k*plane:(k+1)*plane], uint8(*rc.PadValue), width, h)
			}
			rc.Input.normalize(input[k*plane:], crops[i].resize(w, ch), width, h)
		}
		out, dims, err := e.rec.run(input, n, 3, h, width)
		if err != nil {
			return nil, nil, err
		}
		if len(dims) != 3 || dims[0] != n {
			return nil, nil, fmt.Errorf("recognizer output %v, want [%d steps classes]", dims, n)
		}
		steps, classes := dims[1], dims[2]
		if classes < e.classes || (rc.Decoder.Type == "ctc" && classes != e.classes) {
			return nil, nil, errors.New("recognizer has " + fmt.Sprint(classes) + " classes but the character list gives " + fmt.Sprint(e.classes))
		}
		for k, i := range idx {
			texts[i], scores[i] = decode(out[k*steps*classes:(k+1)*steps*classes], steps, classes, e.chars, rc.Decoder)
		}
	}
	return texts, scores, nil
}

// recWidth picks the input width for a batch needing need pixels: at least
// MinWidth, at most MaxWidth, rounded up to a width bucket.
func (e *Engine) recWidth(need int) int {
	rc := e.Manifest.Recognizer
	need = max(need, rc.MinWidth, 8)
	if rc.MaxWidth > 0 {
		need = min(need, rc.MaxWidth)
	}
	for _, w := range rc.Widths {
		if w >= need {
			return w
		}
	}
	if len(rc.Widths) > 0 {
		step := rc.Widths[0]
		return (need + step - 1) / step * step
	}
	return (need + 7) / 8 * 8
}
