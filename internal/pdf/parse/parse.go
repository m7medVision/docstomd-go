package parse

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
)

type Ref struct {
	Num int
	Gen int
}

type Name string

type Stream struct {
	Dict map[string]any
	Raw  []byte
}

var ErrEncrypted = errors.New("pdf: document is encrypted")

type MalformedError struct {
	Detail string
}

func (e *MalformedError) Error() string {
	return "pdf: malformed: " + e.Detail
}

func malformed(detail string) error {
	return &MalformedError{Detail: detail}
}

type parser struct {
	data []byte
	pos  int
}

func (p *parser) skipWhitespace() {
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		if c == 0x00 || c == 0x09 || c == 0x0A || c == 0x0C || c == 0x0D || c == 0x20 {
			p.pos++
			continue
		}
		if c == '%' {
			for p.pos < len(p.data) && p.data[p.pos] != 0x0A && p.data[p.pos] != 0x0D {
				p.pos++
			}
			continue
		}
		return
	}
}

func isDelimiter(c byte) bool {
	switch c {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

func (p *parser) token() (string, bool) {
	p.skipWhitespace()
	start := p.pos
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		if c == 0x00 || c == 0x09 || c == 0x0A || c == 0x0C || c == 0x0D || c == 0x20 || isDelimiter(c) {
			break
		}
		p.pos++
	}
	if p.pos == start {
		return "", false
	}
	return string(p.data[start:p.pos]), true
}

func (p *parser) object() (any, error) {
	p.skipWhitespace()
	if p.pos >= len(p.data) {
		return nil, malformed("unexpected end of file")
	}
	c := p.data[p.pos]
	switch {
	case c == '(':
		return p.literalString()
	case c == '<':
		if p.pos+1 < len(p.data) && p.data[p.pos+1] == '<' {
			return p.dict()
		}
		return p.hexString()
	case c == '[':
		p.pos++
		arr := []any{}
		for {
			p.skipWhitespace()
			if p.pos >= len(p.data) {
				return nil, malformed("unterminated array")
			}
			if p.data[p.pos] == ']' {
				p.pos++
				return arr, nil
			}
			item, err := p.object()
			if err != nil {
				return nil, err
			}
			arr = append(arr, item)
		}
	case c == '/':
		p.pos++
		return p.name(), nil
	case c == '+' || c == '-' || (c >= '0' && c <= '9') || c == '.':
		return p.numberOrRef()
	}
	tok, ok := p.token()
	if !ok {
		return nil, malformed("expected object")
	}
	switch tok {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null":
		return nil, nil
	}
	return nil, malformed("unexpected token " + strconv.Quote(tok))
}

func (p *parser) numberOrRef() (any, error) {
	first, err := p.number()
	if err != nil {
		return nil, err
	}
	num, isInt := first.(int64)
	if !isInt {
		return first, nil
	}
	genPos := p.pos
	p.skipWhitespace()
	if p.pos >= len(p.data) || !startsInteger(p.data[p.pos]) {
		p.pos = genPos
		return first, nil
	}
	second, err := p.number()
	if err != nil {
		p.pos = genPos
		return first, nil
	}
	gen, isInt := second.(int64)
	if !isInt {
		p.pos = genPos
		return first, nil
	}
	p.skipWhitespace()
	if p.pos < len(p.data) && p.data[p.pos] == 'R' && (p.pos+1 >= len(p.data) || p.data[p.pos+1] == 0x00 || p.data[p.pos+1] == 0x09 || p.data[p.pos+1] == 0x0A || p.data[p.pos+1] == 0x0C || p.data[p.pos+1] == 0x0D || p.data[p.pos+1] == 0x20 || isDelimiter(p.data[p.pos+1])) {
		p.pos++
		return Ref{Num: int(num), Gen: int(gen)}, nil
	}
	p.pos = genPos
	return first, nil
}

// smallInteger consumes a token of at most 18 digits with an optional sign
// without allocating; any other token is left for the general number path.
func (p *parser) smallInteger() (int64, bool) {
	i := p.pos
	negative := false
	if i < len(p.data) && (p.data[i] == '+' || p.data[i] == '-') {
		negative = p.data[i] == '-'
		i++
	}
	digitsStart := i
	var value int64
	for i < len(p.data) && p.data[i] >= '0' && p.data[i] <= '9' {
		value = value*10 + int64(p.data[i]-'0')
		i++
	}
	digits := i - digitsStart
	if digits == 0 || digits > 18 {
		return 0, false
	}
	if i < len(p.data) {
		c := p.data[i]
		if !(c == 0x00 || c == 0x09 || c == 0x0A || c == 0x0C || c == 0x0D || c == 0x20 || isDelimiter(c)) {
			return 0, false
		}
	}
	p.pos = i
	if negative {
		value = -value
	}
	return value, true
}

func startsInteger(c byte) bool {
	return c >= '0' && c <= '9' || c == '+' || c == '-'
}

func (p *parser) number() (any, error) {
	p.skipWhitespace()
	if i, ok := p.smallInteger(); ok {
		return i, nil
	}
	tok, ok := p.token()
	if !ok {
		return nil, malformed("expected number")
	}
	if !strings.Contains(tok, ".") {
		if i, err := strconv.ParseInt(tok, 10, 64); err == nil {
			return i, nil
		}
	}
	f, err := strconv.ParseFloat(tok, 64)
	if err != nil {
		return nil, malformed("bad number " + strconv.Quote(tok))
	}
	return f, nil
}

func (p *parser) name() Name {
	var buf []byte
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		if c == 0x00 || c == 0x09 || c == 0x0A || c == 0x0C || c == 0x0D || c == 0x20 || isDelimiter(c) {
			break
		}
		if c == '#' && p.pos+2 < len(p.data) {
			hi := hexDigit(p.data[p.pos+1])
			lo := hexDigit(p.data[p.pos+2])
			if hi >= 0 && lo >= 0 {
				buf = append(buf, byte(hi<<4|lo))
				p.pos += 3
				continue
			}
		}
		buf = append(buf, c)
		p.pos++
	}
	return Name(buf)
}

func hexDigit(c byte) int {
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

func (p *parser) literalString() (any, error) {
	p.pos++
	var buf []byte
	depth := 1
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		switch c {
		case '\\':
			p.pos++
			if p.pos >= len(p.data) {
				return nil, malformed("unterminated string escape")
			}
			e := p.data[p.pos]
			switch e {
			case 'n':
				buf = append(buf, '\n')
			case 'r':
				buf = append(buf, '\r')
			case 't':
				buf = append(buf, '\t')
			case 'b':
				buf = append(buf, '\b')
			case 'f':
				buf = append(buf, '\f')
			case '(', ')', '\\':
				buf = append(buf, e)
			case '\r':
				if p.pos+1 < len(p.data) && p.data[p.pos+1] == '\n' {
					p.pos++
				}
			case '0', '1', '2', '3', '4', '5', '6', '7':
				v := int(e - '0')
				for k := 0; k < 2 && p.pos+1 < len(p.data); k++ {
					n := p.data[p.pos+1]
					if n < '0' || n > '7' {
						break
					}
					v = v*8 | int(n-'0')
					p.pos++
				}
				buf = append(buf, byte(v))
			default:
				buf = append(buf, e)
			}
			p.pos++
		case '(':
			depth++
			buf = append(buf, c)
			p.pos++
		case ')':
			depth--
			if depth == 0 {
				p.pos++
				return buf, nil
			}
			buf = append(buf, c)
			p.pos++
		default:
			buf = append(buf, c)
			p.pos++
		}
	}
	return nil, malformed("unterminated literal string")
}

func (p *parser) hexString() (any, error) {
	p.pos++
	var buf []byte
	var digits []byte
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		if c == '>' {
			p.pos++
			break
		}
		if hexDigit(c) >= 0 {
			digits = append(digits, c)
		}
		p.pos++
	}
	if len(digits)%2 == 1 {
		digits = append(digits, '0')
	}
	for i := 0; i+1 < len(digits); i += 2 {
		buf = append(buf, byte(hexDigit(digits[i])<<4|hexDigit(digits[i+1])))
	}
	return buf, nil
}

func (p *parser) dict() (any, error) {
	p.pos += 2
	d := map[string]any{}
	for {
		p.skipWhitespace()
		if p.pos >= len(p.data) {
			return nil, malformed("unterminated dictionary")
		}
		if p.pos+1 < len(p.data) && p.data[p.pos] == '>' && p.data[p.pos+1] == '>' {
			p.pos += 2
			return d, nil
		}
		if p.data[p.pos] != '/' {
			return nil, malformed("expected name key in dictionary")
		}
		p.pos++
		key := p.name()
		val, err := p.object()
		if err != nil {
			return nil, err
		}
		d[string(key)] = val
	}
}

func (p *parser) indirectObject() (int, int, map[string]any, *Stream, any, error) {
	numTok, ok := p.token()
	if !ok {
		return 0, 0, nil, nil, nil, malformed("expected object number")
	}
	num, err := strconv.Atoi(numTok)
	if err != nil {
		return 0, 0, nil, nil, nil, malformed("bad object number")
	}
	genTok, ok := p.token()
	if !ok {
		return 0, 0, nil, nil, nil, malformed("expected generation")
	}
	gen, err := strconv.Atoi(genTok)
	if err != nil {
		return 0, 0, nil, nil, nil, malformed("bad generation")
	}
	objTok, ok := p.token()
	if !ok || objTok != "obj" {
		return 0, 0, nil, nil, nil, malformed("expected obj keyword")
	}
	body, err := p.object()
	if err != nil {
		return 0, 0, nil, nil, nil, err
	}
	if dict, isDict := body.(map[string]any); isDict {
		p.skipWhitespace()
		if p.pos+8 <= len(p.data) && bytes.HasPrefix(p.data[p.pos:], []byte("stream")) {
			p.pos += 6
			if p.pos < len(p.data) && (p.data[p.pos] == 0x0D) {
				p.pos++
			}
			if p.pos < len(p.data) && p.data[p.pos] == 0x0A {
				p.pos++
			}
			raw, err := p.streamBody(dict["Length"])
			if err != nil {
				return 0, 0, nil, nil, nil, err
			}
			stm := &Stream{Dict: dict, Raw: raw}
			return num, gen, dict, stm, stm, nil
		}
	}
	return num, gen, nil, nil, body, nil
}

func (p *parser) streamBody(lengthHint any) ([]byte, error) {
	if n, ok := lengthHint.(int64); ok && n >= 0 && p.pos+int(n) <= len(p.data) {
		body := p.data[p.pos : p.pos+int(n)]
		probe := p.pos + int(n)
		probeSkip := probe
		for probeSkip < len(p.data) && (p.data[probeSkip] == 0x0D || p.data[probeSkip] == 0x0A) {
			probeSkip++
		}
		if probeSkip+9 <= len(p.data) && bytes.HasPrefix(p.data[probeSkip:], []byte("endstream")) {
			p.pos = probeSkip + len("endstream")
			return body, nil
		}
	}
	end := bytes.Index(p.data[p.pos:], []byte("endstream"))
	if end < 0 {
		return nil, malformed("missing endstream")
	}
	body := p.data[p.pos : p.pos+end]
	p.pos += end + len("endstream")
	return body, nil
}
