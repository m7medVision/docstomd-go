package extract

import (
	"sort"
	"strings"

	"github.com/m7medVision/docstomd-go/internal/pdf/parse"
)

type widthInfo struct {
	widths       map[int]uint16
	defaultWidth uint16
	spaceWidth   uint16
	isCID        bool
	unitsScale   float64
}

func buildWidths(doc *parse.Document, fontDict map[string]any) *widthInfo {
	switch nameOf(fontDict["Subtype"]) {
	case "Type0":
		return parseType0Widths(doc, fontDict)
	case "Type1", "TrueType", "MMType1":
		if info := parseSimpleWidths(doc, fontDict); info != nil {
			return info
		}
		return base14FallbackWidths(doc, fontDict)
	case "Type3":
		return parseSimpleWidths(doc, fontDict)
	}
	return nil
}

func parseSimpleWidths(doc *parse.Document, fontDict map[string]any) *widthInfo {
	firstChar, ok := number(doc.Resolve(fontDict["FirstChar"]))
	if !ok {
		return nil
	}
	lastChar, _ := number(doc.Resolve(fontDict["LastChar"]))
	widthsArr, ok := doc.Resolve(fontDict["Widths"]).([]any)
	if !ok {
		return nil
	}
	widths := map[int]uint16{}
	spaceWidth := uint16(0)
	for i, wObj := range widthsArr {
		code := int(firstChar) + i
		if float64(code) > lastChar {
			break
		}
		w, ok := number(doc.Resolve(wObj))
		if !ok {
			continue
		}
		widths[code] = uint16(w)
		if code == 32 {
			spaceWidth = uint16(w)
		}
	}
	unitsScale := 0.001
	if fm, ok := doc.Resolve(fontDict["FontMatrix"]).([]any); ok && len(fm) > 0 {
		if m, ok := number(doc.Resolve(fm[0])); ok && m != 0 {
			unitsScale = abs(m)
		}
	}
	return &widthInfo{widths: widths, defaultWidth: 500, spaceWidth: spaceWidth, isCID: false, unitsScale: unitsScale}
}

func parseType0Widths(doc *parse.Document, fontDict map[string]any) *widthInfo {
	descArr, ok := doc.Resolve(fontDict["DescendantFonts"]).([]any)
	if !ok || len(descArr) == 0 {
		return nil
	}
	cidDict, ok := dictOf(doc, descArr[0])
	if !ok {
		return nil
	}
	defaultWidth := uint16(1000)
	if dw, ok := number(doc.Resolve(cidDict["DW"])); ok {
		defaultWidth = uint16(dw)
	}
	widths := map[int]uint16{}
	if wArr, ok := doc.Resolve(cidDict["W"]).([]any); ok {
		parseCIDWArray(doc, wArr, widths)
	}
	spaceWidth := uint16(250)
	if w, ok := widths[32]; ok {
		spaceWidth = w
	} else if w, ok := widths[3]; ok {
		spaceWidth = w
	} else if defaultWidth > 0 {
		spaceWidth = defaultWidth / 4
	}
	return &widthInfo{widths: widths, defaultWidth: defaultWidth, spaceWidth: spaceWidth, isCID: true, unitsScale: 0.001}
}

func parseCIDWArray(doc *parse.Document, wArr []any, widths map[int]uint16) {
	i := 0
	for i < len(wArr) {
		start, ok := number(doc.Resolve(wArr[i]))
		if !ok {
			i++
			continue
		}
		if i+1 < len(wArr) {
			if group, ok := doc.Resolve(wArr[i+1]).([]any); ok {
				for j, wObj := range group {
					if w, ok := number(doc.Resolve(wObj)); ok {
						widths[int(start)+j] = uint16(w)
					}
				}
				i += 2
				continue
			}
			if i+2 < len(wArr) {
				if end, okE := number(doc.Resolve(wArr[i+1])); okE {
					if w, okW := number(doc.Resolve(wArr[i+2])); okW {
						for cid := int(start); cid <= int(end); cid++ {
							widths[cid] = uint16(w)
						}
					}
					i += 3
					continue
				}
			}
		}
		i++
	}
}

func base14FallbackWidths(doc *parse.Document, fontDict map[string]any) *widthInfo {
	baseFont := string(nameOf(fontDict["BaseFont"]))
	table := base14TableFor(baseFont)
	if table == nil {
		return nil
	}
	encMap := encodingFor(doc, fontDict)
	widths := map[int]uint16{}
	for code := 0; code <= 255; code++ {
		ch := rune(decodeFallbackChar(byte(code)))
		if c, ok := encMap[byte(code)]; ok {
			ch = c
		} else if nameOf(fontDict["BaseFont"]) == "Symbol" || nameOf(fontDict["BaseFont"]) == "ZapfDingbats" {
			if c, ok := builtinEncodingChar(baseFont, byte(code)); ok {
				ch = c
			}
		}
		if w, ok := base14CharWidthOf(table, ch); ok {
			widths[code] = w
		}
	}
	spaceWidth := uint16(250)
	if w, ok := widths[32]; ok {
		spaceWidth = w
	}
	return &widthInfo{widths: widths, defaultWidth: 500, spaceWidth: spaceWidth, isCID: false, unitsScale: 0.001}
}

func dictOf(doc *parse.Document, obj any) (map[string]any, bool) {
	switch v := obj.(type) {
	case map[string]any:
		return v, true
	case parse.Ref:
		resolved, err := doc.GetObject(v.Num)
		if err == nil {
			if d, ok := resolved.(map[string]any); ok {
				return d, true
			}
		}
	}
	return nil, false
}

func computeStringWidthTS(raw []byte, info *widthInfo, fontSize, charSpacing, wordSpacing float64) float64 {
	total := 0.0
	numSpaces := 0
	numChars := 0
	if info.isCID {
		for j := 0; j+1 < len(raw); j += 2 {
			cid := int(raw[j])<<8 | int(raw[j+1])
			total += float64(cidWidth(info, cid))
			if cid == 32 {
				numSpaces++
			}
			numChars++
		}
	} else {
		for _, b := range raw {
			total += float64(cidWidth(info, int(b)))
			if b == 0x20 {
				numSpaces++
			}
			numChars++
		}
	}
	return total*info.unitsScale*fontSize + float64(numChars)*charSpacing + float64(numSpaces)*wordSpacing
}

func cidWidth(info *widthInfo, code int) uint16 {
	if w, ok := info.widths[code]; ok {
		return w
	}
	return info.defaultWidth
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func base14TableFor(baseFont string) []charWidth {
	name := baseFont
	if idx := strings.IndexByte(name, '+'); idx == 6 {
		name = name[7:]
	}
	lower := toLower(name)
	bold := contains(lower, "bold")
	italic := contains(lower, "italic") || contains(lower, "oblique")
	switch lower {
	case "zapfdingbats", "dingbats", "itczapfdingbats", "zapfdingbatsitc":
		return base14ZAPFDINGBATS
	case "symbol", "symbolmt", "symbolitc":
		return base14SYMBOL
	}
	switch {
	case contains(lower, "courier"):
		return base14COURIER
	case contains(lower, "helvetica") || contains(lower, "arial"):
		if bold && italic {
			return base14HELVETICA_BOLDOBLIQUE
		}
		if bold {
			return base14HELVETICA_BOLD
		}
		if italic {
			return base14HELVETICA_OBLIQUE
		}
		return base14HELVETICA
	case contains(lower, "times"):
		if bold && italic {
			return base14TIMES_BOLDITALIC
		}
		if bold {
			return base14TIMES_BOLD
		}
		if italic {
			return base14TIMES_ITALIC
		}
		return base14TIMES_ROMAN
	}
	return nil
}

func base14CharWidthOf(table []charWidth, ch rune) (uint16, bool) {
	switch ch {
	case 0x00A0:
		ch = ' '
	case 0x00AD:
		ch = '-'
	}
	i := sort.Search(len(table), func(i int) bool { return table[i].ch >= ch })
	if i < len(table) && table[i].ch == ch {
		return table[i].w, true
	}
	return 0, false
}

func builtinEncodingChar(baseFont string, code byte) (rune, bool) {
	lower := toLower(baseFont)
	var table []codeChar
	switch {
	case lower == "symbol" || lower == "symbolmt" || lower == "symbolitc":
		table = symbolEncodingTable
	case lower == "zapfdingbats" || lower == "dingbats" || lower == "itczapfdingbats" || lower == "zapfdingbatsitc":
		table = zapfEncodingTable
	default:
		return 0, false
	}
	i := sort.Search(len(table), func(i int) bool { return table[i].code >= code })
	if i < len(table) && table[i].code == code {
		return table[i].ch, true
	}
	return 0, false
}

func toLower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
