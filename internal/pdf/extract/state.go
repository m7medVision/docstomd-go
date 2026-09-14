package extract

import (
	"math"

	"github.com/m7medVision/docstomd-go/internal/pdf/parse"
)

type gfxState struct {
	ctm         mat
	renderMode  int
	lineWidth   float64
	charSpacing float64
	wordSpacing float64
	horizScale  float64
	textRise    float64
	textLeading float64
	font        string
	fontSize    float64
}

type interp struct {
	doc                 *parse.Document
	pageNum             int
	fonts               map[string]*fontContext
	items               []TextItem
	rects               []Rect
	lines               []Line
	painted             []Rect
	strokeLines         []Line
	ctm                 mat
	textMat             mat
	lineMat             mat
	inText              bool
	renderMode          int
	lineWidth           float64
	charSpace           float64
	wordSpace           float64
	horizScale          float64
	textRise            float64
	leading             float64
	font                string
	fontSize            float64
	mcid                *int64
	mcidStack           []mcidEntry
	gstack              []gfxState
	resources           []map[string]any
	visitedForms        map[parse.Ref]bool
	ops                 int
	pendingPaint        []Rect
	pendingLinesList    [][4]float64
	pathStart, pathLast *[2]float64
}

type mcidEntry struct {
	actualText string
	mcid       *int64
}

func newInterp(doc *parse.Document, pageNum int, resources []map[string]any, fonts map[string]*fontContext) *interp {
	return &interp{
		doc:          doc,
		pageNum:      pageNum,
		fonts:        fonts,
		resources:    resources,
		ctm:          mat{1, 0, 0, 1, 0, 0},
		textMat:      mat{1, 0, 0, 1, 0, 0},
		lineMat:      mat{1, 0, 0, 1, 0, 0},
		horizScale:   1,
		fontSize:     12,
		lineWidth:    1,
		visitedForms: map[parse.Ref]bool{},
	}
}

func (it *interp) run(content []byte) {
	sc := &opScanner{data: content}
	for {
		op, ok := sc.next()
		if !ok {
			return
		}
		it.ops++
		if it.ops > maxPageOperations {
			return
		}
		it.apply(op)
	}
}

func (it *interp) apply(op contentOp) {
	switch op.operator {
	case "q":
		it.gstack = append(it.gstack, gfxState{it.ctm, it.renderMode, it.lineWidth, it.charSpace, it.wordSpace, it.horizScale, it.textRise, it.leading, it.font, it.fontSize})
	case "Q":
		if n := len(it.gstack); n > 0 {
			s := it.gstack[n-1]
			it.gstack = it.gstack[:n-1]
			it.ctm, it.renderMode, it.lineWidth = s.ctm, s.renderMode, s.lineWidth
			it.charSpace, it.wordSpace, it.horizScale = s.charSpacing, s.wordSpacing, s.horizScale
			it.textRise, it.leading, it.font, it.fontSize = s.textRise, s.textLeading, s.font, s.fontSize
		}
	case "cm":
		if len(op.operands) >= 6 {
			var m mat
			for i := 0; i < 6; i++ {
				v, _ := operandFloat(op.operands[i])
				m[i] = v
			}
			it.ctm = matMul(m, it.ctm)
		}
	case "BT":
		it.inText = true
		it.textMat = mat{1, 0, 0, 1, 0, 0}
		it.lineMat = mat{1, 0, 0, 1, 0, 0}
	case "ET":
		it.inText = false
	case "Tf":
		if len(op.operands) >= 2 {
			if n, ok := op.operands[0].(parse.Name); ok {
				it.font = string(n)
			}
			sz, _ := operandFloat(op.operands[1])
			it.fontSize = sz
		}
	case "Tc":
		if len(op.operands) >= 1 {
			it.charSpace, _ = operandFloat(op.operands[0])
		}
	case "Tw":
		if len(op.operands) >= 1 {
			it.wordSpace, _ = operandFloat(op.operands[0])
		}
	case "Tz":
		if len(op.operands) >= 1 {
			v, _ := operandFloat(op.operands[0])
			it.horizScale = v / 100
		}
	case "TL":
		if len(op.operands) >= 1 {
			it.leading, _ = operandFloat(op.operands[0])
		}
	case "Ts":
		if len(op.operands) >= 1 {
			it.textRise, _ = operandFloat(op.operands[0])
		}
	case "Tr":
		if len(op.operands) >= 1 {
			v, _ := operandFloat(op.operands[0])
			it.renderMode = int(v)
		}
	case "Td":
		if len(op.operands) >= 2 {
			tx, _ := operandFloat(op.operands[0])
			ty, _ := operandFloat(op.operands[1])
			m := mat{1, 0, 0, 1, tx, ty}
			it.lineMat = matMul(m, it.lineMat)
			it.textMat = it.lineMat
		}
	case "TD":
		if len(op.operands) >= 2 {
			tx, _ := operandFloat(op.operands[0])
			ty, _ := operandFloat(op.operands[1])
			it.leading = -ty
			m := mat{1, 0, 0, 1, tx, ty}
			it.lineMat = matMul(m, it.lineMat)
			it.textMat = it.lineMat
		}
	case "Tm":
		if len(op.operands) >= 6 {
			var m mat
			for i := 0; i < 6; i++ {
				v, _ := operandFloat(op.operands[i])
				m[i] = v
			}
			it.textMat = m
			it.lineMat = m
		}
	case "T*":
		m := mat{1, 0, 0, 1, 0, -it.leading}
		it.lineMat = matMul(m, it.lineMat)
		it.textMat = it.lineMat
	case "Tj", "'", "\"":
		if len(op.operands) >= 1 {
			if op.operator == "'" {
				m := mat{1, 0, 0, 1, 0, -it.leading}
				it.lineMat = matMul(m, it.lineMat)
				it.textMat = it.lineMat
			} else if op.operator == "\"" && len(op.operands) >= 3 {
				it.wordSpace, _ = operandFloat(op.operands[0])
				it.charSpace, _ = operandFloat(op.operands[1])
			}
			idx := 0
			if op.operator == "\"" {
				idx = 2
			}
			if raw, ok := operandBytes(op.operands[idx]); ok {
				it.showText(raw)
			}
		}
	case "TJ":
		if len(op.operands) >= 1 {
			if arr, ok := docResolveArr(it.doc, op.operands[0]); ok {
				it.showArray(arr)
			}
		}
	case "re":
		if len(op.operands) >= 4 {
			x, _ := operandFloat(op.operands[0])
			y, _ := operandFloat(op.operands[1])
			w, _ := operandFloat(op.operands[2])
			h, _ := operandFloat(op.operands[3])
			p1 := transformPoint(it.ctm, x, y)
			p2 := transformPoint(it.ctm, x+w, y+h)
			rx, ry := math.Min(p1[0], p2[0]), math.Min(p1[1], p2[1])
			rw, rh := math.Abs(p2[0]-p1[0]), math.Abs(p2[1]-p1[1])
			it.rects = append(it.rects, Rect{X: rx, Y: ry, Width: rw, Height: rh, Page: it.pageNum})
			it.pendingPaint = append(it.pendingPaint, Rect{X: rx, Y: ry, Width: rw, Height: rh, Page: it.pageNum})
		}
	case "m", "l":
		if len(op.operands) >= 2 {
			x, _ := operandFloat(op.operands[0])
			y, _ := operandFloat(op.operands[1])
			p := transformPoint(it.ctm, x, y)
			pp := &[2]float64{p[0], p[1]}
			if op.operator == "m" {
				it.pathStart = pp
			} else if it.pathStart != nil {
				it.pendingLinesList = append(it.pendingLinesList, [4]float64{it.pathStart[0], it.pathStart[1], p[0], p[1]})
			}
			it.pathLast = pp
		}
	case "S", "s":
		if op.operator == "s" && it.pathLast != nil && it.pathStart != nil {
			it.pendingLinesList = append(it.pendingLinesList, [4]float64{it.pathLast[0], it.pathLast[1], it.pathStart[0], it.pathStart[1]})
		}
		for _, l := range it.pendingLinesList {
			it.lines = append(it.lines, Line{X1: l[0], Y1: l[1], X2: l[2], Y2: l[3], Page: it.pageNum})
			it.strokeLines = append(it.strokeLines, Line{X1: l[0], Y1: l[1], X2: l[2], Y2: l[3], Page: it.pageNum})
		}
		it.pendingLinesList = nil
		it.pathStart, it.pathLast = nil, nil
	case "f", "F", "f*", "B", "B*", "b", "b*":
		it.painted = append(it.painted, it.pendingPaint...)
		it.pendingPaint = nil
		it.pathStart, it.pathLast = nil, nil
	case "n":
		it.pendingPaint = nil
		it.pendingLinesList = nil
		it.pathStart, it.pathLast = nil, nil
	case "w":
		if len(op.operands) >= 1 {
			v, _ := operandFloat(op.operands[0])
			scale := math.Sqrt(math.Abs(it.ctm[0]*it.ctm[3] - it.ctm[1]*it.ctm[2]))
			it.lineWidth = v * scale
		}
	case "BDC", "BMC":
		var entry mcidEntry
		if len(op.operands) == 2 {
			if p, ok := docResolveDict(it.doc, op.operands[1]); ok {
				if v, ok := number(it.doc.Resolve(p["MCID"])); ok {
					m := int64(v)
					entry.mcid = &m
				}
				if at, ok := docResolveArr(it.doc, p["ActualText"]); ok && len(at) > 0 {
					entry.actualText = actualTextString(at)
				}
			}
		}
		it.mcidStack = append(it.mcidStack, entry)
		it.mcid = entry.mcid
	case "EMC":
		if n := len(it.mcidStack); n > 0 {
			it.mcidStack = it.mcidStack[:n-1]
		}
		if len(it.mcidStack) > 0 {
			it.mcid = it.mcidStack[len(it.mcidStack)-1].mcid
		} else {
			it.mcid = nil
		}
	case "Do":
		if len(op.operands) >= 1 {
			if n, ok := op.operands[0].(parse.Name); ok {
				it.doXObject(string(n))
			}
		}
	}
}

func docResolveArr(doc *parse.Document, obj any) ([]any, bool) {
	arr, ok := doc.Resolve(obj).([]any)
	return arr, ok
}

func docResolveDict(doc *parse.Document, obj any) (map[string]any, bool) {
	d, ok := doc.Resolve(obj).(map[string]any)
	return d, ok
}

func actualTextString(arr []any) string {
	var raw []byte
	for _, item := range arr {
		if b, ok := item.([]byte); ok {
			raw = append(raw, b...)
		}
	}
	if len(raw) >= 2 && raw[0] == 0xFE && raw[1] == 0xFF {
		var sb []rune
		for i := 2; i+1 < len(raw); i += 2 {
			sb = append(sb, rune(int(raw[i])<<8|int(raw[i+1])))
		}
		return string(sb)
	}
	return string(raw)
}

func transformPoint(m mat, x, y float64) [2]float64 {
	return [2]float64{m[0]*x + m[2]*y + m[4], m[1]*x + m[3]*y + m[5]}
}
