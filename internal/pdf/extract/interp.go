package extract

import (
	"math"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/m7medVision/docstomd-go/internal/pdf/parse"
)

const maxPageOperations = 2_000_000

type contentOp struct {
	operands []any
	operator string
}

type opScanner struct {
	data []byte
	pos  int
}

func (s *opScanner) next() (contentOp, bool) {
	var operands []any
	for {
		s.skipSpace()
		if s.pos >= len(s.data) {
			if len(operands) > 0 {
				return contentOp{operands: operands, operator: ""}, true
			}
			return contentOp{}, false
		}
		c := s.data[s.pos]
		if c == '%' {
			for s.pos < len(s.data) && s.data[s.pos] != '\n' && s.data[s.pos] != '\r' {
				s.pos++
			}
			continue
		}
		if isOpDelim(c) || (c >= '0' && c <= '9') || c == '+' || c == '-' || c == '.' {
			obj, err := s.parseOperand()
			if err != nil {
				continue
			}
			operands = append(operands, obj)
			continue
		}
		start := s.pos
		for s.pos < len(s.data) && !isOpDelim(s.data[s.pos]) && !isPDFWhitespaceByte(s.data[s.pos]) {
			s.pos++
		}
		tok := string(s.data[start:s.pos])
		if tok == "true" || tok == "false" || tok == "null" {
			operands = append(operands, tok)
			continue
		}
		return contentOp{operands: operands, operator: tok}, true
	}
}

func isOpDelim(c byte) bool {
	switch c {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

func isPDFWhitespaceByte(c byte) bool {
	return c == 0x00 || c == 0x09 || c == 0x0A || c == 0x0C || c == 0x0D || c == 0x20
}

func (s *opScanner) skipSpace() {
	for s.pos < len(s.data) {
		c := s.data[s.pos]
		if isPDFWhitespaceByte(c) {
			s.pos++
			continue
		}
		if c == '%' {
			for s.pos < len(s.data) && s.data[s.pos] != '\n' && s.data[s.pos] != '\r' {
				s.pos++
			}
			continue
		}
		return
	}
}

func (s *opScanner) parseOperand() (any, error) {
	p := parse.NewParserAt(s.data, s.pos)
	obj, err := p.Object()
	if err != nil {
		s.pos++
		return nil, err
	}
	s.pos = p.Pos()
	return obj, nil
}

func operandBytes(obj any) ([]byte, bool) {
	if b, ok := obj.([]byte); ok {
		return b, true
	}
	return nil, false
}

type mat [6]float64

func matMul(a, b mat) mat {
	return mat{
		a[0]*b[0] + a[1]*b[2],
		a[0]*b[1] + a[1]*b[3],
		a[2]*b[0] + a[3]*b[2],
		a[2]*b[1] + a[3]*b[3],
		a[4]*b[0] + a[5]*b[2] + b[4],
		a[4]*b[1] + a[5]*b[3] + b[5],
	}
}

func riseAdjusted(tm mat, rise float64) mat {
	out := tm
	out[4] += tm[2] * rise
	out[5] += tm[3] * rise
	return out
}

func effectiveFontSize(size float64, combined mat) float64 {
	sx := math.Hypot(combined[0], combined[1])
	sy := math.Hypot(combined[2], combined[3])
	scale := sx
	if sy > sx {
		scale = sy
	}
	return size * scale
}

type runGeometry struct {
	x, y, width, height float64
	rotation            float64
	advanceKnown        bool
}

func baselineRotation(a, b float64) float64 {
	deg := math.Atan2(b, a) * 180 / math.Pi
	for deg < 0 {
		deg += 360
	}
	for deg >= 360 {
		deg -= 360
	}
	return deg
}

func scaledRunGeometry(combined mat, advanceTS *float64, fallbackTS, em, horizScale float64) runGeometry {
	reflected := combined
	if horizScale < 0 {
		reflected[0] = -reflected[0]
		reflected[1] = -reflected[1]
	}
	var adv *float64
	if advanceTS != nil {
		scaled := *advanceTS * math.Abs(horizScale)
		adv = &scaled
	}
	fb := fallbackTS * math.Abs(horizScale)
	return runGeometryOf(reflected, adv, fb, em)
}

func runGeometryOf(combined mat, advanceTS *float64, fallbackTS, em float64) runGeometry {
	x0, y0 := combined[4], combined[5]
	advance, known := fallbackTS, false
	if advanceTS != nil {
		advance, known = *advanceTS, true
	}
	reversed := em < 0
	em = math.Abs(em)
	ax := advance * combined[0]
	ay := advance * combined[1]
	axisLen := math.Hypot(combined[0], combined[1])
	var ux, uy float64
	if axisLen > 1e-12 {
		px, py := -combined[1]/axisLen, combined[0]/axisLen
		yAxisPerp := combined[2]*px + combined[3]*py
		maxScale := math.Max(axisLen, math.Hypot(combined[2], combined[3]))
		emPerp := em
		if math.Abs(yAxisPerp) > 1e-12 && maxScale > 1e-12 {
			emPerp = em * math.Abs(yAxisPerp) / maxScale
		}
		side := 1.0
		if yAxisPerp < 0 {
			side = -1.0
		}
		if reversed {
			side = -side
		}
		ux = px * emPerp * side
		uy = py * emPerp * side
	} else {
		uy = em
		if reversed {
			uy = -em
		}
	}
	dirX, dirY := combined[0], combined[1]
	if reversed {
		dirX, dirY = -dirX, -dirY
	}
	det := combined[0]*combined[3] - combined[1]*combined[2]
	var rotation float64
	if det < 0 {
		upSign := 1.0
		if reversed {
			upSign = -1.0
		}
		upX, upY := combined[2]*upSign, combined[3]*upSign
		rotation = baselineRotation(upY, -upX)
	} else {
		rotation = baselineRotation(dirX, dirY)
	}
	xs := []float64{x0, x0 + ax, x0 + ux, x0 + ax + ux}
	ys := []float64{y0, y0 + ay, y0 + uy, y0 + ay + uy}
	xMin, xMax := math.Inf(1), math.Inf(-1)
	yMin, yMax := math.Inf(1), math.Inf(-1)
	for _, v := range xs {
		xMin = math.Min(xMin, v)
		xMax = math.Max(xMax, v)
	}
	for _, v := range ys {
		yMin = math.Min(yMin, v)
		yMax = math.Max(yMax, v)
	}
	return runGeometry{x: xMin, y: yMin, width: xMax - xMin, height: yMax - yMin, rotation: rotation, advanceKnown: known}
}

func expandLigatures(text string) string {
	replaced := false
	for _, c := range text {
		if c < 0x20 && c != '\n' && c != '\r' && c != '\t' {
			replaced = true
			break
		}
	}
	if replaced {
		var sb strings.Builder
		for _, c := range text {
			if c >= ' ' || c == '\n' || c == '\r' || c == '\t' {
				sb.WriteRune(c)
			}
		}
		text = sb.String()
	}
	hadPresentationForms := false
	for _, c := range text {
		if isArabicPresentationForm(c) {
			hadPresentationForms = true
			break
		}
	}
	if hadPresentationForms {
		text = norm.NFKC.String(text)
	}
	var sb strings.Builder
	sb.Grow(len(text))
	for _, c := range text {
		switch {
		case c >= '\u2000' && c <= '\u200A':
			sb.WriteByte(' ')
		case c == '\u00AD' || c == '\u200B' || c == '\uFEFF' || c == '\u200C' || c == '\u200D' || c == '\u2060':
		case c == '\uFB00':
			sb.WriteString("ff")
		case c == '\uFB01':
			sb.WriteString("fi")
		case c == '\uFB02':
			sb.WriteString("fl")
		case c == '\uFB03':
			sb.WriteString("ffi")
		case c == '\uFB04':
			sb.WriteString("ffl")
		case c == '\uFB05' || c == '\uFB06':
			sb.WriteString("st")
		default:
			sb.WriteRune(c)
		}
	}
	result := sb.String()
	if hadPresentationForms {
		result = reverseVisualArabic(result)
	}
	return result
}

func isArabicPresentationForm(c rune) bool {
	return c >= 0xFB50 && c <= 0xFDFF || c >= 0xFE70 && c <= 0xFEFE
}

func isArabicIndicDigit(c rune) bool {
	return c >= 0x0660 && c <= 0x0669 || c >= 0x06F0 && c <= 0x06F9
}

func isArabicNumericSeparator(c rune) bool {
	return c == 0x066B || c == 0x066C
}

func isForwardAlnum(c rune) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || isArabicIndicDigit(c)
}

func isAdjacentToAlnum(chars []rune, idx int) bool {
	return idx > 0 && isForwardAlnum(chars[idx-1]) ||
		idx+1 < len(chars) && isForwardAlnum(chars[idx+1])
}

func isASCIIPunct(c rune) bool {
	return c >= '!' && c <= '/' || c >= ':' && c <= '@' || c >= '[' && c <= '`' || c >= '{' && c <= '~'
}

func reverseVisualArabic(text string) string {
	hasLTR := false
	for _, c := range text {
		if isForwardAlnum(c) {
			hasLTR = true
			break
		}
	}
	if !hasLTR {
		return reverseKeepingMarks(text)
	}
	chars := []rune(text)
	isLRTPunct := func(i int) bool {
		return (isASCIIPunct(chars[i]) || isArabicNumericSeparator(chars[i])) && isAdjacentToAlnum(chars, i)
	}
	type run struct {
		ltr     bool
		content []rune
	}
	var runs []run
	i := 0
	for i < len(chars) {
		ltr := isForwardAlnum(chars[i]) || isLRTPunct(i)
		var content []rune
		for i < len(chars) {
			cLTR := isForwardAlnum(chars[i]) || isLRTPunct(i)
			if cLTR != ltr {
				break
			}
			content = append(content, chars[i])
			i++
		}
		runs = append(runs, run{ltr: ltr, content: content})
	}
	for a, b := 0, len(runs)-1; a < b; a, b = a+1, b-1 {
		runs[a], runs[b] = runs[b], runs[a]
	}
	var sb strings.Builder
	sb.Grow(len(text))
	for _, r := range runs {
		if r.ltr {
			sb.WriteString(string(r.content))
		} else {
			sb.WriteString(reverseKeepingMarks(string(r.content)))
		}
	}
	return sb.String()
}

func reverseKeepingMarks(text string) string {
	chars := []rune(text)
	var sb strings.Builder
	sb.Grow(len(text))
	end := len(chars)
	for i := len(chars) - 1; i >= 0; i-- {
		if i == 0 || !isCombiningMark(chars[i]) {
			for _, c := range chars[i:end] {
				sb.WriteRune(mirrorBracket(c))
			}
			end = i
		}
	}
	return sb.String()
}

func mirrorBracket(c rune) rune {
	switch c {
	case '(':
		return ')'
	case ')':
		return '('
	case '[':
		return ']'
	case ']':
		return '['
	case '{':
		return '}'
	case '}':
		return '{'
	case '<':
		return '>'
	case '>':
		return '<'
	}
	return c
}

func isCombiningMark(c rune) bool {
	if unicode.In(c, unicode.Mn, unicode.Mc, unicode.Me) {
		return true
	}
	return norm.NFKC.PropertiesString(string(c)).CCC() != 0
}

func isBoldFontName(name string) bool {
	l := toLower(name)
	return strings.Contains(l, "bold") || strings.Contains(l, "-bd") || strings.Contains(l, "_bd") ||
		strings.Contains(l, "black") || strings.Contains(l, "heavy") || strings.Contains(l, "demibold") ||
		strings.Contains(l, "semibold") || strings.Contains(l, "demi-bold") || strings.Contains(l, "semi-bold") ||
		strings.Contains(l, "extrabold") || strings.Contains(l, "ultrabold") ||
		(strings.Contains(l, "medium") && !strings.Contains(l, "mediumitalic")) ||
		(strings.Contains(l, "-medi") && !strings.Contains(l, "mediumital"))
}

func isItalicFontName(name string) bool {
	l := toLower(name)
	return strings.Contains(l, "italic") || strings.Contains(l, "oblique") || strings.Contains(l, "-it") ||
		strings.Contains(l, "_it") || strings.Contains(l, "slant") || strings.Contains(l, "inclined") || strings.Contains(l, "kursiv")
}
