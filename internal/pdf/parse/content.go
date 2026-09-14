package parse

// Parser exposes the object parser for content-stream tokenization.
type Parser struct {
	inner parser
}

// NewParserAt creates a parser over data starting at pos.
func NewParserAt(data []byte, pos int) *Parser {
	return &Parser{inner: parser{data: data, pos: pos}}
}

// Object parses the next PDF object.
func (p *Parser) Object() (any, error) {
	return p.inner.object()
}

// Pos returns the current byte position.
func (p *Parser) Pos() int { return p.inner.pos }
