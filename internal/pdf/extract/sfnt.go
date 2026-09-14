package extract

import "encoding/binary"

// sfntGIDToUnicode extracts a GID→rune map from the cmap table of an
// embedded TrueType/OpenType font, using Unicode subtables (format 0, 4, 6,
// 12).
func sfntGIDToUnicode(data []byte) map[int]rune {
	if len(data) < 12 {
		return nil
	}
	numTables := int(binary.BigEndian.Uint16(data[4:6]))
	out := map[int]rune{}
	for i := 0; i < numTables; i++ {
		rec := 12 + i*16
		if rec+16 > len(data) {
			return nil
		}
		if string(data[rec:rec+4]) != "cmap" {
			continue
		}
		offset := int(binary.BigEndian.Uint32(data[rec+8 : rec+12]))
		if offset+4 > len(data) {
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
			unicode := platform == 0 || (platform == 3 && (encoding == 1 || encoding == 10))
			if !unicode || subOff+2 > len(data) {
				continue
			}
			readSubtable(data[subOff:], out)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func readSubtable(sub []byte, out map[int]rune) {
	if len(sub) < 2 {
		return
	}
	format := int(binary.BigEndian.Uint16(sub[0:2]))
	switch format {
	case 0:
		if len(sub) < 262 {
			return
		}
		for code, b := range sub[6:262] {
			if b != 0 {
				out[int(b)] = rune(code)
			}
		}
	case 4:
		if len(sub) < 14 {
			return
		}
		segCount := int(binary.BigEndian.Uint16(sub[6:8])) / 2
		if segCount == 0 || 16+segCount*8 > len(sub) {
			return
		}
		endCodes := 14
		reserved := endCodes + segCount*2
		startCodes := reserved + 2
		idDeltas := startCodes + segCount*2
		idRange := idDeltas + segCount*2
		for i := 0; i < segCount; i++ {
			start := int(binary.BigEndian.Uint16(sub[startCodes+i*2 : startCodes+i*2+2]))
			end := int(binary.BigEndian.Uint16(sub[endCodes+i*2 : endCodes+i*2+2]))
			delta := int(int16(binary.BigEndian.Uint16(sub[idDeltas+i*2 : idDeltas+i*2+2])))
			rangeOff := idRange + i*2
			for c := start; c <= end && c != 0xFFFF; c++ {
				var gid int
				if binary.BigEndian.Uint16(sub[rangeOff:rangeOff+2]) == 0 {
					gid = (c + delta) & 0xFFFF
				} else {
					glyphOff := rangeOff + int(binary.BigEndian.Uint16(sub[rangeOff:rangeOff+2])) + (c-start)*2
					if glyphOff+2 > len(sub) {
						continue
					}
					gid = int(binary.BigEndian.Uint16(sub[glyphOff : glyphOff+2]))
					if gid == 0 {
						continue
					}
					gid = (gid + delta) & 0xFFFF
				}
				if gid != 0 {
					out[gid] = rune(c)
				}
				if c == end {
					break
				}
			}
		}
	case 6:
		if len(sub) < 10 {
			return
		}
		first := int(binary.BigEndian.Uint16(sub[6:8]))
		count := int(binary.BigEndian.Uint16(sub[8:10]))
		for i := 0; i < count; i++ {
			off := 10 + i*2
			if off+2 > len(sub) {
				break
			}
			gid := int(binary.BigEndian.Uint16(sub[off : off+2]))
			if gid != 0 {
				out[gid] = rune(first + i)
			}
		}
	case 12:
		if len(sub) < 16 {
			return
		}
		nGroups := int(binary.BigEndian.Uint32(sub[12:16]))
		for g := 0; g < nGroups; g++ {
			off := 16 + g*12
			if off+12 > len(sub) {
				break
			}
			start := int(binary.BigEndian.Uint32(sub[off : off+4]))
			end := int(binary.BigEndian.Uint32(sub[off+4 : off+8]))
			startGID := int(binary.BigEndian.Uint32(sub[off+8 : off+12]))
			if end-start > 0x10000 {
				continue
			}
			for c := start; c <= end; c++ {
				out[startGID+c-start] = rune(c)
			}
		}
	}
}
