package parse

import (
	"bytes"
	"compress/zlib"
	"io"
	"strconv"
)

func (d *Document) decodeStream(stm *Stream, dict map[string]any) ([]byte, error) {
	data := stm.Raw
	var filters []any
	switch f := dict["Filter"].(type) {
	case Name:
		filters = []any{f}
	case []any:
		filters = f
	}
	var parms []any
	switch p := dict["DecodeParms"].(type) {
	case map[string]any:
		parms = []any{p}
	case []any:
		parms = p
	}
	for i, f := range filters {
		name, ok := f.(Name)
		if !ok {
			continue
		}
		var parm map[string]any
		if i < len(parms) {
			if pm, ok := parms[i].(map[string]any); ok {
				parm = pm
			}
		}
		var err error
		switch name {
		case "FlateDecode":
			data, err = d.flateDecode(data)
			if err != nil {
				return nil, err
			}
			data, err = applyPredictor(data, parm)
			if err != nil {
				return nil, err
			}
		case "ASCIIHexDecode":
			data, err = asciiHexDecode(data)
			if err != nil {
				return nil, err
			}
		case "ASCII85Decode":
			data, err = ascii85Decode(data)
			if err != nil {
				return nil, err
			}
		default:
			return nil, malformed("unsupported stream filter " + string(name))
		}
	}
	return data, nil
}

type inflater interface {
	io.Reader
	zlib.Resetter
}

// Flate output per stream is capped at flateExpansion times the file size,
// kept between flateMinLimit and flateMaxLimit, so a small file can't inflate
// (and cache) gigabytes.
const (
	flateExpansion = 100
	flateMinLimit  = 16 << 20
	flateMaxLimit  = 256 << 20
)

func flateLimit(fileSize int) int64 {
	return min(max(int64(fileSize)*flateExpansion, flateMinLimit), flateMaxLimit)
}

// flateDecode reuses one zlib reader per document; allocating its window
// per stream dominated decode cost.
func (d *Document) flateDecode(data []byte) ([]byte, error) {
	src := bytes.NewReader(data)
	if d.inflater == nil {
		r, err := zlib.NewReader(src)
		if err != nil {
			return nil, malformed("bad FlateDecode stream")
		}
		d.inflater = r.(inflater)
	} else if err := d.inflater.Reset(src, nil); err != nil {
		return nil, malformed("bad FlateDecode stream")
	}
	limit := flateLimit(len(d.data))
	out, err := io.ReadAll(io.LimitReader(d.inflater, limit+1))
	if int64(len(out)) > limit {
		return nil, malformed("FlateDecode output exceeds the " + strconv.FormatInt(limit, 10) + "-byte limit")
	}
	if err != nil && len(out) == 0 {
		return nil, malformed("bad FlateDecode data")
	}
	return out, nil
}

func applyPredictor(data []byte, parm map[string]any) ([]byte, error) {
	if parm == nil {
		return data, nil
	}
	predictor, ok := parm["Predictor"].(int64)
	// 10-15 are the PNG predictors; every one tags each row with its own
	// filter type, so they share one decoder.
	if !ok || predictor < 10 || predictor > 15 {
		return data, nil
	}
	colors := int64(1)
	if n, ok := parm["Colors"].(int64); ok {
		colors = n
	}
	bpc := int64(8)
	if n, ok := parm["BitsPerComponent"].(int64); ok {
		bpc = n
	}
	columns := int64(1)
	if n, ok := parm["Columns"].(int64); ok {
		columns = n
	}
	if colors < 1 || colors > 32 {
		return nil, malformed("predictor Colors out of range")
	}
	switch bpc {
	case 1, 2, 4, 8, 16:
	default:
		return nil, malformed("predictor BitsPerComponent out of range")
	}
	if columns < 1 {
		return nil, malformed("predictor Columns out of range")
	}
	// Rows longer than the data can't decode; checking columns first keeps
	// the row-length product from overflowing.
	if columns > int64(len(data))*8 {
		return []byte{}, nil
	}
	rowLen64 := (colors*bpc*columns + 7) / 8
	if rowLen64 >= int64(len(data)) {
		return []byte{}, nil
	}
	rowLen := int(rowLen64)
	bpp := int((colors*bpc + 7) / 8)
	out := make([]byte, 0, len(data))
	prev := make([]byte, rowLen)
	pos := 0
	for pos+1 <= len(data) {
		filter := data[pos]
		pos++
		if pos+rowLen > len(data) {
			break
		}
		row := make([]byte, rowLen)
		copy(row, data[pos:pos+rowLen])
		pos += rowLen
		switch filter {
		case 0:
		case 1:
			for i := bpp; i < rowLen; i++ {
				row[i] += row[i-bpp]
			}
		case 2:
			for i := 0; i < rowLen; i++ {
				row[i] += prev[i]
			}
		case 3:
			for i := 0; i < rowLen; i++ {
				var left byte
				if i >= bpp {
					left = row[i-bpp]
				}
				row[i] += byte((int(left) + int(prev[i])) / 2)
			}
		case 4:
			for i := 0; i < rowLen; i++ {
				var a, c byte
				if i >= bpp {
					a = row[i-bpp]
					c = prev[i-bpp]
				}
				b := prev[i]
				row[i] += paeth(a, b, c)
			}
		}
		out = append(out, row...)
		copy(prev, row)
	}
	return out, nil
}

func paeth(a, b, c byte) byte {
	p := int(a) + int(b) - int(c)
	pa, pb, pc := abs(p-int(a)), abs(p-int(b)), abs(p-int(c))
	if pa <= pb && pa <= pc {
		return a
	}
	if pb <= pc {
		return b
	}
	return c
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func asciiHexDecode(data []byte) ([]byte, error) {
	var digits []byte
	for _, c := range data {
		if c == '>' {
			break
		}
		if hexDigit(c) >= 0 {
			digits = append(digits, c)
		}
	}
	if len(digits)%2 == 1 {
		digits = append(digits, '0')
	}
	out := make([]byte, 0, len(digits)/2)
	for i := 0; i+1 < len(digits); i += 2 {
		out = append(out, byte(hexDigit(digits[i])<<4|hexDigit(digits[i+1])))
	}
	return out, nil
}

func ascii85Decode(data []byte) ([]byte, error) {
	var out []byte
	var group [5]byte
	n := 0
	for i := 0; i < len(data); i++ {
		c := data[i]
		if c == '~' {
			break
		}
		if c == 'z' && n == 0 {
			out = append(out, 0, 0, 0, 0)
			continue
		}
		if c == 0x20 || c == 0x09 || c == 0x0A || c == 0x0D {
			continue
		}
		if c < '!' || c > 'u' {
			return nil, malformed("bad ASCII85 data")
		}
		group[n] = c - '!'
		n++
		if n == 5 {
			out = append(out, a85Group(group)...)
			n = 0
		}
	}
	if n > 0 {
		for i := n; i < 5; i++ {
			group[i] = 84
		}
		full := a85Group(group)
		out = append(out, full[:n-1]...)
	}
	return out, nil
}

func a85Group(g [5]byte) []byte {
	v := uint32(0)
	for i := 0; i < 5; i++ {
		v = v*85 + uint32(g[i])
	}
	return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
}
