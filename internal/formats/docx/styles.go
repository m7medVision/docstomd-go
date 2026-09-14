package docx

import (
	"strconv"
	"strings"

	"github.com/m7medVision/docstomd-go/internal/model"
	"github.com/m7medVision/docstomd-go/internal/opc"
)

// toggles is the parity of true bold/italic/strike specifications along a
// style chain. These are toggle properties (ECMA-376 §17.7.3): within the
// style hierarchy a true specification flips the inherited value and a false
// one leaves it unchanged, so the parity is XORed over the docDefaults base.
// Direct run formatting is absolute on/off instead.
type toggles struct {
	bold, italic, strike bool
}

func (t toggles) over(base model.Style) model.Style {
	base.Bold = base.Bold != t.bold
	base.Italic = base.Italic != t.italic
	base.Strike = base.Strike != t.strike
	return base
}

type styleDef struct {
	elem   *opc.Element
	parent string
}

type styles struct {
	defs        map[string]styleDef
	docDefaults model.Style
}

func parseStyles(root *opc.Element) *styles {
	s := &styles{defs: map[string]styleDef{}}
	if root == nil {
		return s
	}
	for style := range root.Children(nsW, "style") {
		if id, ok := style.Attr(nsW, "styleId"); ok {
			parent, _ := val(style, "basedOn")
			s.defs[id] = styleDef{elem: style, parent: parent}
		}
	}
	if rpr := child(child(child(root, "docDefaults"), "rPrDefault"), "rPr"); rpr != nil {
		s.docDefaults = applyDirect(rpr, model.Style{})
	}
	return s
}

// walk visits a style's basedOn chain child to root until visit returns
// true. A dangling reference ends the walk; a cycle is malformed.
func (s *styles) walk(id string, visit func(*opc.Element) bool) error {
	seen := map[string]bool{}
	for {
		def, ok := s.defs[id]
		if !ok {
			return nil
		}
		if seen[id] {
			return &opc.MalformedError{Detail: "style inheritance cycle at " + strconv.Quote(id)}
		}
		seen[id] = true
		if visit(def.elem) {
			return nil
		}
		id = def.parent
	}
}

func (s *styles) runToggles(id string) (toggles, error) {
	var t toggles
	err := s.walk(id, func(style *opc.Element) bool {
		if rpr := child(style, "rPr"); rpr != nil {
			b, _ := onOff(rpr, "b")
			i, _ := onOff(rpr, "i")
			st, _ := onOff(rpr, "strike")
			ds, _ := onOff(rpr, "dstrike")
			t.bold = t.bold != b
			t.italic = t.italic != i
			t.strike = t.strike != (st || ds)
		}
		return false
	})
	return t, err
}

// headingLevel resolves a paragraph style's heading level from its name
// ("heading N", "Title") or outlineLvl through basedOn. found reports the
// nearest specification; level 0 is the explicit off value (outlineLvl 9),
// which stops inheritance.
func (s *styles) headingLevel(id string) (level int, found bool, err error) {
	err = s.walk(id, func(style *opc.Element) bool {
		name, _ := val(style, "name")
		name = strings.ToLower(name)
		if rest, ok := strings.CutPrefix(name, "heading "); ok {
			if n, err := strconv.ParseUint(strings.TrimSpace(rest), 10, 8); err == nil {
				level, found = int(n), true
				return true
			}
		}
		if name == "title" {
			level, found = 1, true
			return true
		}
		if n, ok := outlineLevel(child(style, "pPr")); ok {
			level, found = n, true
			return true
		}
		return false
	})
	return level, found, err
}

// outlineLevel reads w:outlineLvl as a 1-based heading level, 0 for the
// explicit "no outline level" value 9.
func outlineLevel(ppr *opc.Element) (int, bool) {
	v, ok := val(ppr, "outlineLvl")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseUint(v, 10, 8)
	if err != nil {
		return 0, false
	}
	if n < 9 {
		return int(n) + 1, true
	}
	return 0, true
}

func (s *styles) blockStyle(id string) (blockKind, error) {
	var kind blockKind
	err := s.walk(id, func(style *opc.Element) bool {
		name, ok := val(style, "name")
		if ok {
			kind = blockKindOf(name)
		}
		return kind != noBlock
	})
	return kind, err
}

// styleNumID is the numId a paragraph style contributes through basedOn. An
// ilvl in a style's numPr is ignored (ECMA-376 §17.3.1.19); the level comes
// from the abstract levels' pStyle bindings.
func (s *styles) styleNumID(id string) (numID int, found bool, err error) {
	err = s.walk(id, func(style *opc.Element) bool {
		numID, found = parseNumID(child(child(style, "pPr"), "numPr"))
		return found
	})
	return numID, found, err
}

func (s *styles) styleNumberingLevel(id string, inst *instance) (level int, found bool, err error) {
	err = s.walk(id, func(style *opc.Element) bool {
		styleID, ok := style.Attr(nsW, "styleId")
		if !ok {
			return false
		}
		level, found = inst.styleLevel(styleID)
		return found
	})
	return level, found, err
}

// directNumID is the numId a numbering style's own numPr references, without
// inheritance (the numStyleLink contract).
func (s *styles) directNumID(id string) (int, bool) {
	def, ok := s.defs[id]
	if !ok {
		return 0, false
	}
	return parseNumID(child(child(def.elem, "pPr"), "numPr"))
}

func parseNumID(numPr *opc.Element) (int, bool) {
	v, ok := val(numPr, "numId")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseUint(v, 10, 31)
	return int(n), err == nil
}

// applyDirect overlays direct w:rPr formatting, which is absolute on/off.
func applyDirect(rpr *opc.Element, base model.Style) model.Style {
	if on, ok := onOff(rpr, "b"); ok {
		base.Bold = on
	}
	if on, ok := onOff(rpr, "i"); ok {
		base.Italic = on
	}
	s, sok := onOff(rpr, "strike")
	d, dok := onOff(rpr, "dstrike")
	if sok || dok {
		base.Strike = s || d
	}
	return base
}
