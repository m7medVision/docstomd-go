package extract

import (
	"math"
	"strings"

	"github.com/m7medVision/docstomd-go/internal/pdf/parse"
)

// fontContext carries every decode input for one page font resource.
type fontContext struct {
	baseFont               string
	widths                 *widthInfo
	enc                    map[byte]rune
	toUnicode              *cmap
	fallback               *cmap
	hasCMap                bool
	styleBold, styleItalic bool
}

func buildFontContext(doc *parse.Document, fontDict map[string]any) *fontContext {
	fc := &fontContext{
		baseFont: string(nameOf(fontDict["BaseFont"])),
		widths:   buildWidths(doc, fontDict),
		enc:      encodingFor(doc, fontDict),
	}
	if tu := doc.Resolve(fontDict["ToUnicode"]); tu != nil {
		var data []byte
		switch v := tu.(type) {
		case *parse.Stream:
			data, _ = doc.StreamData(v)
		case parse.Ref:
			if obj, err := doc.GetObject(v.Num); err == nil {
				if stm, ok := obj.(*parse.Stream); ok {
					data, _ = doc.StreamData(stm)
				}
			}
		}
		if len(data) > 0 {
			fc.toUnicode = parseToUnicodeCMap(data)
			fc.hasCMap = fc.toUnicode != nil && (len(fc.toUnicode.entries) > 0)
		}
	}
	if fc.widths != nil && fc.widths.isCID {
		if fc.toUnicode == nil {
			if ff := fontFileData(doc, fontDict); ff != nil {
				if fb := buildCMapFallbackFromFont(ff); fb != nil && len(fb.entries) > 0 {
					fc.fallback = fb
					fc.hasCMap = true
				}
			}
		}
	}
	if desc, ok := dictOf(doc, fontDict["FontDescriptor"]); ok {
		flags, _ := number(doc.Resolve(desc["Flags"]))
		if int64(flags)&64 != 0 {
			fc.styleItalic = true
		}
		if int64(flags)&0x40000 != 0 {
			fc.styleBold = true
		}
	}
	return fc
}

func fontFileData(doc *parse.Document, fontDict map[string]any) []byte {
	descFonts, ok := docResolveArr(doc, fontDict["DescendantFonts"])
	if !ok || len(descFonts) == 0 {
		return nil
	}
	cidDict, ok := dictOf(doc, descFonts[0])
	if !ok {
		return nil
	}
	desc, ok := dictOf(doc, cidDict["FontDescriptor"])
	if !ok {
		return nil
	}
	for _, key := range []string{"FontFile2", "FontFile3"} {
		if ref, ok := doc.Resolve(desc[key]).(parse.Ref); ok {
			if obj, err := doc.GetObject(ref.Num); err == nil {
				if stm, ok := obj.(*parse.Stream); ok {
					if data, ok := doc.StreamData(stm); ok {
						return data
					}
					return stm.Raw
				}
			}
		}
	}
	return nil
}

func textScore(s string) int {
	score := 0
	for _, c := range s {
		if c == 0xFFFD {
			score -= 5
		} else if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == ' ' {
			score++
		} else if c < 0x20 {
			score -= 3
		}
	}
	return score
}

// decodeRaw walks the font's decode ladder: ToUnicode, embedded cmap,
// Differences, then byte-level fallbacks.
func (fc *fontContext) decodeRaw(doc *parse.Document, raw []byte) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	if fc.toUnicode != nil {
		if !fc.toUnicode.twoByte && fc.enc != nil {
			var sb strings.Builder
			found := false
			for _, b := range raw {
				if s, ok := fc.toUnicode.entries[int(b)]; ok && !strings.ContainsRune(s, 0xFFFD) {
					sb.WriteString(s)
					found = true
					continue
				}
				if fb := fc.fallback; fb != nil {
					if s, ok := fb.entries[int(b)]; ok && !strings.ContainsRune(s, 0xFFFD) {
						sb.WriteString(s)
						found = true
						continue
					}
				}
				if ch, ok := fc.enc[b]; ok {
					sb.WriteRune(ch)
					found = true
					continue
				}
				if b >= 0x20 {
					sb.WriteRune(decodeFallbackChar(b))
					found = true
				}
			}
			if found {
				return sb.String(), true
			}
			return "", false
		}
		primary := fc.toUnicode.decode(raw)
		if fc.fallback != nil {
			fb := fc.fallback.decode(raw)
			expected := len(raw) / 2
			if fb != "" && (primary == "" || len([]rune(primary))*2 < expected || textScore(fb) > textScore(primary)+3) {
				return fb, true
			}
		}
		if primary != "" {
			return primary, true
		}
	}
	if fc.widths != nil && fc.widths.isCID {
		hasHigh := false
		for _, b := range raw {
			if b > 0x7F {
				hasHigh = true
				break
			}
		}
		if hasHigh {
			count := len(raw) / 2
			if count < 1 {
				count = 1
			}
			return strings.Repeat("\uFFFD", count), true
		}
	}
	if fc.enc != nil {
		var sb strings.Builder
		found := false
		for _, b := range raw {
			if ch, ok := fc.enc[b]; ok {
				sb.WriteRune(ch)
				found = true
			} else if b >= 0x20 {
				sb.WriteRune(decodeFallbackChar(b))
				found = true
			}
		}
		if found {
			return sb.String(), true
		}
	}
	if len(raw) >= 2 && raw[0] == 0xFE && raw[1] == 0xFF {
		return utf16BytesToString(raw[2:]), true
	}
	if len(raw) >= 4 && len(raw)%2 == 0 {
		nulls := 0
		for _, b := range raw {
			if b == 0 {
				nulls++
			}
		}
		if nulls*4 > len(raw) {
			text := utf16BytesToString(raw)
			if textScore(text) > 0 {
				return text, true
			}
		}
	}
	hasHigh := false
	for _, b := range raw {
		if b > 0x7F {
			hasHigh = true
			break
		}
	}
	if hasHigh && isValidUTF8(raw) {
		return string(raw), true
	}
	var sb strings.Builder
	found := false
	for _, b := range raw {
		if b >= 0x20 {
			sb.WriteRune(decodeFallbackChar(b))
			found = true
		}
	}
	if found {
		return sb.String(), true
	}
	return "", false
}

func isValidUTF8(b []byte) bool {
	i := 0
	for i < len(b) {
		c := b[i]
		switch {
		case c < 0x80:
			i++
		case c >= 0xC2 && c <= 0xDF && i+1 < len(b):
			i += 2
		case c >= 0xE0 && c <= 0xEF && i+2 < len(b):
			i += 3
		case c >= 0xF0 && c <= 0xF4 && i+3 < len(b):
			i += 4
		default:
			return false
		}
	}
	return i == len(b)
}

func (it *interp) showText(raw []byte) {
	fc := it.fonts[it.font]
	var widthTS *float64
	if fc != nil && fc.widths != nil {
		w := computeStringWidthTS(raw, fc.widths, it.fontSize, it.charSpace, it.wordSpace)
		widthTS = &w
	}
	combined := matMul(riseAdjusted(it.textMat, it.textRise), it.ctm)
	rendered := effectiveFontSize(it.fontSize, combined)
	estimate := estimatedAdvanceTS(raw, it.fontSize)
	geometry := scaledRunGeometry(combined, widthTS, estimate, rendered, it.horizScale)
	cursor := estimate
	if widthTS != nil {
		cursor = *widthTS
	}
	it.textMat[4] += cursor * it.horizScale * it.textMat[0]
	it.textMat[5] += cursor * it.horizScale * it.textMat[1]
	if fc == nil {
		return
	}
	if it.renderMode == 3 {
		return
	}
	text, ok := fc.decodeRaw(it.doc, raw)
	if !ok || strings.TrimSpace(text) == "" {
		return
	}
	baseFont := fc.baseFont
	if baseFont == "" {
		baseFont = it.font
	}
	it.items = append(it.items, TextItem{
		Text:         expandLigatures(text),
		X:            geometry.x,
		Y:            geometry.y,
		Width:        geometry.width,
		Height:       geometry.height,
		Rotation:     geometry.rotation,
		AdvanceKnown: geometry.advanceKnown,
		Font:         itemFontName(it.font, baseFont),
		FontTag:      it.font,
		FontSize:     rendered,
		Page:         it.pageNum,
		IsBold:       isBoldFontName(baseFont) || fc.styleBold,
		IsItalic:     isItalicFontName(baseFont) || fc.styleItalic,
		ItemType:     ItemText,
		MCID:         it.mcid,
	})
}

func estimatedAdvanceTS(raw []byte, fontSize float64) float64 {
	glyphs := 0
	for _, b := range raw {
		if b != 0x20 {
			glyphs++
		}
	}
	return float64(glyphs) * fontSize * 0.5
}

func itemFontName(resource, baseFont string) string {
	if len(resource) >= 3 && (resource[:2] == "C0" || resource[:2] == "C2") && resource[2] == '_' {
		return resource
	}
	return baseFont
}

// showArray renders a TJ array: strings with kerning offsets, splitting runs
// at column-sized gaps and inserting spaces at word-gap offsets.
func (it *interp) showArray(arr []any) {
	fc := it.fonts[it.font]
	spaceThreshold := 120.0
	if fc != nil && fc.widths != nil {
		spaceEm := float64(fc.widths.spaceWidth) * fc.widths.unitsScale
		spaceThreshold = math.Max(spaceEm*1000*0.4, 80)
	}
	columnGap := spaceThreshold * 4

	type subItem struct {
		text         string
		startTS      float64
		endTS        float64
		advanceKnown bool
	}
	var subs []subItem
	var curText string
	var curStart, widthTS float64
	painted := false
	curAdvanceKnown := true

	flush := func() {
		if curText == "" {
			return
		}
		subs = append(subs, subItem{text: curText, startTS: curStart, endTS: widthTS, advanceKnown: curAdvanceKnown})
		curText = ""
		curAdvanceKnown = true
	}

	for _, el := range arr {
		switch docResolve(it.doc, el).(type) {
		case int64, float64:
			n, _ := operandFloat(docResolve(it.doc, el))
			displacement := -n / 1000 * it.fontSize
			if n < -columnGap && curText != "" {
				flush()
				widthTS += displacement
				curStart = widthTS
				painted = false
			} else {
				widthTS += displacement
				if n < -spaceThreshold && curText != "" && !strings.HasSuffix(curText, " ") {
					curText += " "
				}
			}
		case []byte:
			raw := docResolve(it.doc, el).([]byte)
			if len(raw) == 0 {
				continue
			}
			if !painted {
				curStart = widthTS
				painted = true
			}
			if fc != nil && fc.widths != nil {
				w := computeStringWidthTS(raw, fc.widths, it.fontSize, it.charSpace, it.wordSpace)
				widthTS += w
			} else {
				widthTS += estimatedAdvanceTS(raw, it.fontSize)
				curAdvanceKnown = false
			}
			if fc != nil {
				if text, ok := fc.decodeRaw(it.doc, raw); ok {
					curText += text
				}
			}
		}
	}
	flush()

	for _, sub := range subs {
		it.showDecodedAt(sub.text, sub.startTS, sub.endTS, sub.advanceKnown)
	}
}

// showDecodedAt renders a TJ sub-run with pre-decoded text and pre-measured
// advance window.
func (it *interp) showDecodedAt(text string, startTS, endTS float64, advanceKnown bool) {
	if strings.TrimSpace(text) == "" {
		return
	}
	fc := it.fonts[it.font]
	saved := it.textMat
	it.textMat[4] += startTS * it.horizScale * it.textMat[0]
	it.textMat[5] += startTS * it.horizScale * it.textMat[1]
	combined := matMul(riseAdjusted(it.textMat, it.textRise), it.ctm)
	rendered := effectiveFontSize(it.fontSize, combined)
	advance := (endTS - startTS) * math.Abs(it.horizScale)
	geometry := scaledRunGeometry(combined, &advance, advance, rendered, it.horizScale)
	if !advanceKnown {
		geometry = scaledRunGeometry(combined, nil, advance, rendered, it.horizScale)
	}
	it.textMat = saved
	if it.renderMode == 3 || fc == nil {
		return
	}
	baseFont := fc.baseFont
	if baseFont == "" {
		baseFont = it.font
	}
	it.items = append(it.items, TextItem{
		Text:         expandLigatures(text),
		X:            geometry.x,
		Y:            geometry.y,
		Width:        geometry.width,
		Height:       geometry.height,
		Rotation:     geometry.rotation,
		AdvanceKnown: geometry.advanceKnown,
		Font:         itemFontName(it.font, baseFont),
		FontTag:      it.font,
		FontSize:     rendered,
		Page:         it.pageNum,
		IsBold:       isBoldFontName(baseFont) || fc.styleBold,
		IsItalic:     isItalicFontName(baseFont) || fc.styleItalic,
		ItemType:     ItemText,
		MCID:         it.mcid,
	})
}

func (it *interp) doXObject(name string) {
	for _, res := range it.resources {
		xobjs, ok := dictOf(it.doc, res["XObject"])
		if !ok {
			continue
		}
		ref, ok := xobjs[name].(parse.Ref)
		if !ok {
			continue
		}
		if it.visitedForms[ref] {
			return
		}
		it.visitedForms[ref] = true
		defer delete(it.visitedForms, ref)
		obj, err := it.doc.GetObject(ref.Num)
		if err != nil {
			return
		}
		stm, ok := obj.(*parse.Stream)
		if !ok {
			return
		}
		switch nameOf(stm.Dict["Subtype"]) {
		case "Form":
			if formRes, ok := dictOf(it.doc, stm.Dict["Resources"]); ok {
				savedRes := it.resources
				it.resources = []map[string]any{formRes}
				content, _ := it.doc.StreamData(stm)
				it.run(content)
				it.resources = savedRes
			} else {
				content, _ := it.doc.StreamData(stm)
				it.run(content)
			}
		case "Image":
			width, _ := number(stm.Dict["Width"])
			height, _ := number(stm.Dict["Height"])
			if width <= 0 || height <= 0 {
				return
			}
			p1 := transformPoint(it.ctm, 0, 0)
			p2 := transformPoint(it.ctm, 1, 1)
			it.items = append(it.items, TextItem{
				Text:         "[Image: " + name + "]",
				Page:         it.pageNum,
				X:            math.Min(p1[0], p2[0]),
				Y:            math.Min(p1[1], p2[1]),
				Width:        math.Abs(p2[0] - p1[0]),
				Height:       math.Abs(p2[1] - p1[1]),
				AdvanceKnown: true,
				ItemType:     ItemImage,
				MCID:         it.mcid,
			})
		}
		return
	}
}

func utf16BytesToString(raw []byte) string {
	var sb []rune
	for i := 0; i+1 < len(raw); i += 2 {
		sb = append(sb, rune(int(raw[i])<<8|int(raw[i+1])))
	}
	return string(sb)
}

func docResolve(doc *parse.Document, obj any) any { return doc.Resolve(obj) }
