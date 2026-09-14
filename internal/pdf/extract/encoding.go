package extract

import (
	"strings"

	"github.com/m7medVision/docstomd-go/internal/pdf/parse"
)

// encodingFor resolves a simple font's byte→rune map: /Encoding name or
// dictionary with /Differences over the base encoding, glyph names through
// the Adobe Glyph List.
func encodingFor(doc *parse.Document, fontDict map[string]any) map[byte]rune {
	base := standardEncoding
	symbolic := false
	switch subtype := nameOf(fontDict["Subtype"]); subtype {
	case "TrueType":
		base = winAnsiEncoding
	}
	flags, ok := number(fontDict["Flags"])
	if ok && (int64(flags)&32) != 0 {
		symbolic = true
	}
	encObj := fontDict["Encoding"]
	if ref, isRef := encObj.(parse.Ref); isRef {
		resolved, err := doc.GetObject(ref.Num)
		if err == nil {
			encObj = resolved
		} else {
			encObj = nil
		}
	}
	switch enc := doc.Resolve(encObj).(type) {
	case parse.Name:
		switch enc {
		case "WinAnsiEncoding":
			base = winAnsiEncoding
		case "MacRomanEncoding":
			base = macRomanEncoding
		case "StandardEncoding":
			base = standardEncoding
		}
	case map[string]any:
		if be, ok := doc.Resolve(enc["BaseEncoding"]).(parse.Name); ok {
			switch be {
			case "WinAnsiEncoding":
				base = winAnsiEncoding
			case "MacRomanEncoding":
				base = macRomanEncoding
			case "StandardEncoding":
				base = standardEncoding
			}
		}
		if diffs, ok := doc.Resolve(enc["Differences"]).([]any); ok {
			merged := cloneEncoding(base)
			if merged == nil {
				merged = map[byte]rune{}
			}
			code := 0
			for _, item := range diffs {
				switch v := doc.Resolve(item).(type) {
				case int64:
					code = int(v)
				case parse.Name:
					if code >= 0 && code <= 255 {
						merged[byte(code)] = glyphToChar(string(v))
					}
					code++
				}
			}
			return merged
		}
	}
	if symbolic && base == nil {
		return nil
	}
	return cloneEncoding(base)
}

func cloneEncoding(src map[byte]rune) map[byte]rune {
	if src == nil {
		return nil
	}
	out := make(map[byte]rune, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// glyphToChar resolves a glyph name through the AGL with the spec's
// suffix-stripping rule and uniXXXX form.
func glyphToChar(name string) rune {
	if r, ok := glyphToUnicode[name]; ok {
		return r
	}
	if idx := strings.IndexByte(name, '.'); idx > 0 {
		if r, ok := glyphToUnicode[name[:idx]]; ok {
			return r
		}
	}
	if strings.HasPrefix(name, "uni") && len(name) == 7 {
		var v int64
		for _, c := range name[3:] {
			d := hexVal(byte(c))
			if d < 0 {
				return rune(0xFFFD)
			}
			v = v<<4 | int64(d)
		}
		if v > 0 && v <= 0x10FFFF {
			return rune(v)
		}
	}
	return rune(0xFFFD)
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// decodeFallbackChar is the single-byte decode fallback: cp1252 punctuation
// for 0x80..0x9F, Latin-1 elsewhere.
func decodeFallbackChar(code byte) rune {
	if cp, ok := cp1252High[code-0x80]; ok {
		return cp
	}
	return rune(code)
}

func nameOf(obj any) parse.Name {
	if n, ok := obj.(parse.Name); ok {
		return n
	}
	return ""
}

func number(obj any) (float64, bool) {
	switch v := obj.(type) {
	case int64:
		return float64(v), true
	case float64:
		return v, true
	}
	return 0, false
}
