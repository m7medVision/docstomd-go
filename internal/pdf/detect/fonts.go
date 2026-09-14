package detect

import (
	"encoding/binary"
	"slices"

	"github.com/m7medVision/docstomd-go/internal/pdf/parse"
)

type fontInfo struct {
	subtype      parse.Name
	encoding     parse.Name
	hasToUnicode bool
	dict         map[string]any
}

const maxCIDWExpansion = 4096

func collectFontsFromResources(doc *parse.Document, resources map[string]any, fontMap map[parse.Ref]*fontInfo) {
	fontDict, ok := dictOf(doc, resources["Font"])
	if !ok {
		return
	}
	for _, value := range fontDict {
		fontRef, ok := value.(parse.Ref)
		if !ok {
			continue
		}
		if _, exists := fontMap[fontRef]; exists {
			continue
		}
		obj, err := doc.GetObject(fontRef.Num)
		if err != nil {
			continue
		}
		fd, ok := obj.(map[string]any)
		if !ok {
			continue
		}
		fontMap[fontRef] = &fontInfo{
			subtype:      nameOf(fd["Subtype"]),
			encoding:     nameOf(fd["Encoding"]),
			hasToUnicode: fd["ToUnicode"] != nil,
			dict:         fd,
		}
	}
}

func nameOf(obj any) parse.Name {
	if n, ok := obj.(parse.Name); ok {
		return n
	}
	return ""
}

func resolveFontNamesScoped(doc *parse.Document, scopes []map[string]any, names map[string]bool, usedFontIDs map[parse.Ref]bool) {
	for name := range names {
		for _, resources := range scopes {
			if fontDict, ok := dictOf(doc, resources["Font"]); ok {
				if ref, ok := fontDict[name].(parse.Ref); ok {
					usedFontIDs[ref] = true
					break
				}
			}
		}
	}
}

func resolveFontNamesUnscoped(doc *parse.Document, resources map[string]any, names map[string]bool, usedFontIDs map[parse.Ref]bool) {
	fontDict, ok := dictOf(doc, resources["Font"])
	if !ok {
		return
	}
	for name := range names {
		if ref, ok := fontDict[name].(parse.Ref); ok {
			usedFontIDs[ref] = true
		}
	}
}

func usedFontsHaveIdentityHNoToUnicode(doc *parse.Document, usedFontIDs map[parse.Ref]bool, fontMap map[parse.Ref]*fontInfo) bool {
	hasUndecodableIdentityH := false
	hasOtherDecodableFont := false
	for ref := range usedFontIDs {
		info, ok := fontMap[ref]
		if !ok {
			continue
		}
		switch info.subtype {
		case "Type0":
			if info.encoding != "Identity-H" && info.encoding != "Identity-V" {
				hasOtherDecodableFont = true
				continue
			}
			if info.hasToUnicode {
				hasOtherDecodableFont = true
				continue
			}
			if identityHFontHasFallback(doc, info.dict) {
				hasOtherDecodableFont = true
				continue
			}
			hasUndecodableIdentityH = true
		case "Type3":
		default:
			hasOtherDecodableFont = true
		}
	}
	return hasUndecodableIdentityH && !hasOtherDecodableFont
}

func usedFontsAreOnlyType3(usedFontIDs map[parse.Ref]bool, fontMap map[parse.Ref]*fontInfo) bool {
	if len(usedFontIDs) == 0 {
		return false
	}
	hasType3 := false
	for ref := range usedFontIDs {
		info, ok := fontMap[ref]
		if !ok {
			continue
		}
		if info.subtype == "Type3" {
			if info.hasToUnicode {
				return false
			}
			hasType3 = true
		} else {
			return false
		}
	}
	return hasType3
}

func usedFontsHaveDecodableText(doc *parse.Document, usedFontIDs map[parse.Ref]bool, fontMap map[parse.Ref]*fontInfo) bool {
	for ref := range usedFontIDs {
		info, ok := fontMap[ref]
		if !ok {
			continue
		}
		if info.hasToUnicode {
			return true
		}
		switch info.subtype {
		case "Type1", "TrueType", "MMType1":
			return true
		case "Type0":
			if identityHFontHasFallback(doc, info.dict) {
				return true
			}
		}
	}
	return false
}

func identityHFontHasFallback(doc *parse.Document, fontDict map[string]any) bool {
	cidFontDict, ok := descendantFontDict(doc, fontDict)
	if !ok {
		return false
	}
	if cidValuesLookLikeUnicode(cidFontDict) {
		return true
	}
	if descriptor, ok := dictOf(doc, cidFontDict["FontDescriptor"]); ok {
		fontFileRef, ok := refOf(descriptor["FontFile2"])
		if !ok {
			fontFileRef, ok = refOf(descriptor["FontFile3"])
		}
		if ok {
			if embeddedFontHasCmap(doc, fontFileRef) {
				return true
			}
		}
	}
	return false
}

func descendantFontDict(doc *parse.Document, fontDict map[string]any) (map[string]any, bool) {
	var arr []any
	switch v := doc.Resolve(fontDict["DescendantFonts"]).(type) {
	case []any:
		arr = v
	case parse.Ref:
		return nil, false
	}
	if len(arr) == 0 {
		return nil, false
	}
	if d, ok := dictOf(doc, arr[0]); ok {
		return d, true
	}
	return nil, false
}

func refOf(obj any) (parse.Ref, bool) {
	ref, ok := obj.(parse.Ref)
	return ref, ok
}

func cidValuesLookLikeUnicode(cidFontDict map[string]any) bool {
	wArr, ok := docResolveArr(cidFontDict["W"])
	if !ok {
		return false
	}
	seen := map[uint16]bool{}
	i := 0
	for i < len(wArr) && len(seen) < maxCIDWExpansion {
		cid, ok := wArr[i].(int64)
		if !ok {
			i++
			continue
		}
		start := uint16(cid)
		if i+1 < len(wArr) {
			if widths, ok := wArr[i+1].([]any); ok {
				for j := 0; j < len(widths) && len(seen) < maxCIDWExpansion; j++ {
					seen[start+uint16(j)] = true
				}
				i += 2
			} else {
				if i+2 < len(wArr) {
					if end, ok := wArr[i+1].(int64); ok {
						recordUniqueCIDRange(start, uint16(end), seen)
					}
					i += 3
				} else {
					i++
				}
			}
		} else {
			seen[start] = true
			i++
		}
	}
	if len(seen) == 0 {
		return false
	}
	cids := make([]uint16, 0, len(seen))
	for c := range seen {
		cids = append(cids, c)
	}
	slices.Sort(cids)
	median := cids[len(cids)/2]
	return median >= 0x41
}

func recordUniqueCIDRange(start, end uint16, seen map[uint16]bool) {
	if start > end {
		return
	}
	for cid := start; ; cid++ {
		if len(seen) >= maxCIDWExpansion {
			return
		}
		seen[cid] = true
		if cid == end {
			return
		}
	}
}

func docResolveArr(obj any) ([]any, bool) {
	switch v := obj.(type) {
	case []any:
		return v, true
	case parse.Ref:
		return nil, false
	}
	return nil, false
}

func embeddedFontHasCmap(doc *parse.Document, fontRef parse.Ref) bool {
	obj, err := doc.GetObject(fontRef.Num)
	if err != nil {
		return false
	}
	stm, ok := obj.(*parse.Stream)
	if !ok {
		return false
	}
	data, ok := doc.StreamData(stm)
	if !ok && len(stm.Raw) == 0 {
		return false
	}
	if !ok {
		data = stm.Raw
	}
	return sfntHasUnicodeCmap(data)
}

func sfntHasUnicodeCmap(data []byte) bool {
	if len(data) < 12 {
		return false
	}
	numTables := int(binary.BigEndian.Uint16(data[4:6]))
	tableDir := 12
	if tableDir+numTables*16 > len(data) {
		return false
	}
	for i := 0; i < numTables; i++ {
		rec := tableDir + i*16
		tag := string(data[rec : rec+4])
		if tag != "cmap" {
			continue
		}
		offset := int(binary.BigEndian.Uint32(data[rec+8 : rec+12]))
		length := int(binary.BigEndian.Uint32(data[rec+12 : rec+16]))
		if offset+length > len(data) || offset+4 > len(data) {
			continue
		}
		subCount := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
		for s := 0; s < subCount; s++ {
			recOff := offset + 4 + s*8
			if recOff+8 > len(data) {
				break
			}
			platform := int(binary.BigEndian.Uint16(data[recOff : recOff+2]))
			encoding := int(binary.BigEndian.Uint16(data[recOff+2 : recOff+4]))
			subOff := offset + int(binary.BigEndian.Uint32(data[recOff+4:recOff+8]))
			unicode := platform == 0 || (platform == 3 && encoding == 0)
			if !unicode || subOff+2 > len(data) {
				continue
			}
			if cmapSubtableHasMappings(data[subOff:]) {
				return true
			}
		}
	}
	return false
}

func cmapSubtableHasMappings(sub []byte) bool {
	if len(sub) < 2 {
		return false
	}
	format := int(binary.BigEndian.Uint16(sub[0:2]))
	switch format {
	case 0:
		if len(sub) < 262 {
			return false
		}
		for _, b := range sub[6:262] {
			if b != 0 {
				return true
			}
		}
		return false
	case 4:
		if len(sub) < 14 {
			return false
		}
		segCount := int(binary.BigEndian.Uint16(sub[6:8])) / 2
		if segCount == 0 {
			return false
		}
		endCodes := 14
		startCodes := endCodes + segCount*2 + 2
		for i := 0; i < segCount; i++ {
			end := int(binary.BigEndian.Uint16(sub[endCodes+i*2 : endCodes+i*2+2]))
			if end == 0xFFFF {
				continue
			}
			start := int(binary.BigEndian.Uint16(sub[startCodes+i*2 : startCodes+i*2+2]))
			if end >= start {
				return true
			}
		}
		return false
	case 6:
		if len(sub) < 10 {
			return false
		}
		return binary.BigEndian.Uint16(sub[8:10]) > 0
	case 12:
		if len(sub) < 16 {
			return false
		}
		return binary.BigEndian.Uint32(sub[12:16]) > 0
	}
	return false
}

func documentTitle(doc *parse.Document) *string {
	infoRef, ok := doc.Trailer()["Info"].(parse.Ref)
	if !ok {
		return nil
	}
	obj, err := doc.GetObject(infoRef.Num)
	if err != nil {
		return nil
	}
	info, ok := obj.(map[string]any)
	if !ok {
		return nil
	}
	title, ok := info["Title"].([]byte)
	if !ok {
		return nil
	}
	decoded := decodePDFText(title)
	return &decoded
}

func decodePDFText(b []byte) string {
	if len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF {
		runes := make([]rune, 0, (len(b)-2)/2)
		for i := 2; i+1 < len(b); i += 2 {
			runes = append(runes, rune(int(b[i])<<8|int(b[i+1])))
		}
		return string(runes)
	}
	return string(b)
}
