package extract

import (
	"strings"
)

// cmap maps character codes (1 or 2 bytes) to Unicode strings, parsed from a
// ToUnicode CMap stream.
type cmap struct {
	entries map[int]string
	twoByte bool
}

func parseToUnicodeCMap(data []byte) *cmap {
	text := string(data)
	m := &cmap{entries: map[int]string{}}
	codeLens := map[int]bool{}

	readBlock := func(kind string) {
		open := "begin" + kind
		closeWord := "end" + kind
		for {
			i := strings.Index(text, open)
			if i < 0 {
				return
			}
			j := strings.Index(text[i:], closeWord)
			if j < 0 {
				return
			}
			block := text[i+len(open) : i+j]
			text = text[i+j+len(closeWord):]
			toks := strings.FieldsFunc(block, func(r rune) bool {
				return r == '<' || r == '>' || r == '[' || r == ']' || r == '\n' || r == '\r' || r == '\t' || r == ' '
			})
			if kind == "bfchar" {
				for k := 0; k+1 < len(toks); k += 2 {
					src := parseHexBytes(toks[k])
					if len(src) == 0 {
						continue
					}
					codeLens[len(src)] = true
					m.entries[codeOf(src)] = hexToUnicodeString(toks[k+1])
				}
			} else {
				for k := 0; k+2 < len(toks); k += 3 {
					lo := parseHexBytes(toks[k])
					hi := parseHexBytes(toks[k+1])
					if len(lo) == 0 || len(lo) != len(hi) {
						continue
					}
					codeLens[len(lo)] = true
					loV, hiV := codeOf(lo), codeOf(hi)
					if hiV < loV || hiV-loV > 65535 {
						continue
					}
					dst := parseHexBytes(toks[k+2])
					if len(dst) <= 2 {
						dstCode := codeOf(dst)
						for c := loV; c <= hiV; c++ {
							m.entries[c] = string(rune(dstCode + c - loV))
						}
					} else {
						for c := loV; c <= hiV; c++ {
							m.entries[c] = hexToUnicodeString(toks[k+2])
						}
					}
				}
			}
		}
	}

	readBlock("bfchar")
	readBlock("bfrange")
	m.twoByte = codeLens[2] && !codeLens[1]
	return m
}

func codeOf(b []byte) int {
	v := 0
	for _, x := range b {
		v = v<<8 | int(x)
	}
	return v
}

func parseHexBytes(s string) []byte {
	clean := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, strings.ToUpper(s))
	if len(clean)%2 == 1 {
		clean += "0"
	}
	out := make([]byte, 0, len(clean)/2)
	for i := 0; i+1 < len(clean); i += 2 {
		hi, lo := hexVal(clean[i]), hexVal(clean[i+1])
		if hi < 0 || lo < 0 {
			return out
		}
		out = append(out, byte(hi<<4|lo))
	}
	return out
}

func hexToUnicodeString(s string) string {
	b := parseHexBytes(s)
	var sb strings.Builder
	for i := 0; i+1 < len(b); i += 2 {
		r := rune(int(b[i])<<8 | int(b[i+1]))
		if r == 0 {
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

func (m *cmap) decode(raw []byte) string {
	if m == nil {
		return ""
	}
	if !m.twoByte {
		var sb strings.Builder
		for _, b := range raw {
			if s, ok := m.entries[int(b)]; ok && !strings.ContainsRune(s, 0xFFFD) {
				sb.WriteString(s)
			}
		}
		return sb.String()
	}
	var sb strings.Builder
	for j := 0; j+1 < len(raw); j += 2 {
		if s, ok := m.entries[int(raw[j])<<8|int(raw[j+1])]; ok {
			sb.WriteString(s)
		}
	}
	return sb.String()
}

// buildCMapFallbackFromFont builds a GID→Unicode map from an embedded
// TrueType/OpenType font's cmap table.
func buildCMapFallbackFromFont(data []byte) *cmap {
	gidToRune := sfntGIDToUnicode(data)
	if gidToRune == nil {
		return nil
	}
	m := &cmap{entries: map[int]string{}, twoByte: true}
	for gid, r := range gidToRune {
		m.entries[gid] = string(r)
	}
	return m
}
