package extract

import (
	"strings"
	"unicode/utf16"
)

// cmap maps character codes (1 or 2 bytes) to Unicode strings, parsed from a
// ToUnicode CMap stream.
type cmap struct {
	entries map[int]string
	twoByte bool
}

func parseToUnicodeCMap(data []byte) *cmap {
	m := &cmap{entries: map[int]string{}}
	codeLens := map[int]bool{}
	toks := cmapTokens(string(data))
	for i := 0; i < len(toks); i++ {
		switch toks[i] {
		case "beginbfchar":
			for i++; i+1 < len(toks) && toks[i] != "endbfchar"; i += 2 {
				src := parseHexBytes(toks[i])
				if len(src) == 0 {
					continue
				}
				codeLens[len(src)] = true
				m.entries[codeOf(src)] = hexToUnicodeString(toks[i+1])
			}
		case "beginbfrange":
			for i++; i+2 < len(toks) && toks[i] != "endbfrange"; i += 3 {
				lo, hi := parseHexBytes(toks[i]), parseHexBytes(toks[i+1])
				dst := toks[i+2]
				var arr []string
				if dst == "[" {
					j := i + 3
					for j < len(toks) && toks[j] != "]" {
						j++
					}
					arr = toks[i+3 : j]
					i = j - 2
				}
				if len(lo) == 0 || len(lo) != len(hi) {
					continue
				}
				codeLens[len(lo)] = true
				loV, hiV := codeOf(lo), codeOf(hi)
				if hiV < loV || hiV-loV > 65535 {
					continue
				}
				if arr != nil {
					for k := 0; k < len(arr) && loV+k <= hiV; k++ {
						m.entries[loV+k] = hexToUnicodeString(arr[k])
					}
					continue
				}
				// A range destination increments its last character per code.
				base := []rune(hexToUnicodeString(dst))
				if len(base) == 0 {
					continue
				}
				for c := loV; c <= hiV; c++ {
					r := append([]rune(nil), base...)
					r[len(r)-1] += rune(c - loV)
					m.entries[c] = string(r)
				}
			}
		}
	}
	m.twoByte = codeLens[2] && !codeLens[1]
	return m
}

// cmapTokens splits CMap text into hex strings (without the angle brackets),
// array brackets and bare words.
func cmapTokens(text string) []string {
	var toks []string
	for i := 0; i < len(text); {
		switch c := text[i]; {
		case strings.HasPrefix(text[i:], "<<"):
			i += 2
		case c == '>':
			i++
		case c == '<':
			j := strings.IndexByte(text[i:], '>')
			if j < 0 {
				return toks
			}
			toks = append(toks, text[i+1:i+j])
			i += j + 1
		case c == '[' || c == ']':
			toks = append(toks, text[i:i+1])
			i++
		case c == ' ' || c == '\n' || c == '\r' || c == '\t' || c == '\f' || c == 0:
			i++
		default:
			j := i
			for j < len(text) && !strings.ContainsRune("<>[] \n\r\t\f\x00", rune(text[j])) {
				j++
			}
			if j == i {
				j++
			}
			toks = append(toks, text[i:j])
			i = j
		}
	}
	return toks
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
	if len(b) == 1 {
		return string(rune(b[0]))
	}
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		if u := uint16(b[i])<<8 | uint16(b[i+1]); u != 0 {
			units = append(units, u)
		}
	}
	return string(utf16.Decode(units))
}

func (m *cmap) decode(raw []byte) string {
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
