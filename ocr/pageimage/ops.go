package pageimage

import (
	"bytes"

	"github.com/m7medVision/docstomd-go/internal/pdf/parse"
)

// maxStack bounds q nesting.
const maxStack = 256

type contentOp struct {
	operands []any
	operator string
}

// opScanner splits a content stream into operators and their operands.
type opScanner struct {
	data []byte
	pos  int
}

func (s *opScanner) next() (contentOp, bool) {
	var operands []any
	for {
		s.skipSpace()
		if s.pos >= len(s.data) {
			return contentOp{}, false
		}
		c := s.data[s.pos]
		if isDelim(c) || (c >= '0' && c <= '9') || c == '+' || c == '-' || c == '.' {
			p := parse.NewParserAt(s.data, s.pos)
			obj, err := p.Object()
			if err != nil {
				s.pos++
				continue
			}
			s.pos = p.Pos()
			if len(operands) < 64 {
				operands = append(operands, obj)
			}
			continue
		}
		start := s.pos
		for s.pos < len(s.data) && !isDelim(s.data[s.pos]) && !isSpace(s.data[s.pos]) {
			s.pos++
		}
		tok := string(s.data[start:s.pos])
		switch tok {
		case "true", "false", "null":
			operands = append(operands, tok)
			continue
		case "ID":
			s.skipInlineImage()
		}
		return contentOp{operands: operands, operator: tok}, true
	}
}

// skipInlineImage moves past inline image data up to its EI operator.
func (s *opScanner) skipInlineImage() {
	for i := s.pos; i+2 < len(s.data); i++ {
		if isSpace(s.data[i]) && bytes.HasPrefix(s.data[i+1:], []byte("EI")) && (i+3 >= len(s.data) || isSpace(s.data[i+3]) || isDelim(s.data[i+3])) {
			s.pos = i + 3
			return
		}
	}
	s.pos = len(s.data)
}

func (s *opScanner) skipSpace() {
	for s.pos < len(s.data) {
		c := s.data[s.pos]
		if isSpace(c) {
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

func isDelim(c byte) bool {
	switch c {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

func isSpace(c byte) bool {
	return c == 0x00 || c == 0x09 || c == 0x0A || c == 0x0C || c == 0x0D || c == 0x20
}
