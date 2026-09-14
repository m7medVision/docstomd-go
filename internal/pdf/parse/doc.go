// Package parse provides a minimal, stdlib-only PDF object layer:
// xref, trailer, page tree, and stream decoding.
package parse

import (
	"bytes"
	"strconv"
	"strings"
)

type xrefEntry struct {
	offset    int64
	objStream int
	objIndex  int
	kind      byte
}

type Document struct {
	data     []byte
	xref     map[int]xrefEntry
	trailer  map[string]any
	objs     map[int]any
	pages    []Ref
	pageDict map[int]map[string]any
	streams  map[*Stream]decodedStream
	inflater inflater
}

type decodedStream struct {
	data []byte
	ok   bool
}

func Parse(data []byte) (*Document, error) {
	if !bytes.Contains(data[:min(1024, len(data))], []byte("%PDF-")) {
		return nil, malformed("missing PDF header")
	}
	d := &Document{data: data, xref: map[int]xrefEntry{}, objs: map[int]any{}, pageDict: map[int]map[string]any{}, streams: map[*Stream]decodedStream{}}
	offset, err := d.startXRef()
	if err == nil {
		err = d.loadXRefChain(offset)
	}
	if err != nil {
		if !d.repairByScan() {
			return nil, err
		}
	}
	if _, encrypted := d.trailer["Encrypt"]; encrypted {
		return nil, ErrEncrypted
	}
	if err := d.buildPageTree(); err != nil {
		return nil, err
	}
	return d, nil
}

// repairByScan rebuilds the xref map by scanning for "N G obj" headers when
// the startxref pointer or xref chain is damaged.
func (d *Document) repairByScan() bool {
	re := []byte(" obj")
	pos := 0
	found := false
	for {
		idx := bytes.Index(d.data[pos:], re)
		if idx < 0 {
			break
		}
		at := pos + idx
		lineStart := at
		for lineStart > 0 && d.data[lineStart-1] != 0x0A && d.data[lineStart-1] != 0x0D {
			lineStart--
			if at-lineStart > 20 {
				break
			}
		}
		header := string(d.data[lineStart:at])
		fields := strings.Fields(header)
		if len(fields) == 2 {
			if num, err1 := strconv.Atoi(fields[0]); err1 == nil {
				if _, err2 := strconv.Atoi(fields[1]); err2 == nil {
					if _, exists := d.xref[num]; !exists {
						d.xref[num] = xrefEntry{offset: int64(lineStart), kind: 'n'}
						found = true
					}
				}
			}
		}
		pos = at + len(re)
	}
	if !found {
		return false
	}
	if d.trailer == nil {
		d.trailer = d.findTrailerByScan()
	}
	return d.trailer != nil
}

func (d *Document) findTrailerByScan() map[string]any {
	idx := bytes.LastIndex(d.data, []byte("trailer"))
	if idx < 0 {
		return nil
	}
	p := parser{data: d.data, pos: idx + len("trailer")}
	obj, err := p.object()
	if err != nil {
		return nil
	}
	if td, ok := obj.(map[string]any); ok {
		return td
	}
	return nil
}

func (d *Document) startXRef() (int64, error) {
	tail := d.data
	if len(tail) > 2048 {
		tail = tail[len(tail)-min(2048, len(tail)):]
	}
	idx := bytes.LastIndex(tail, []byte("startxref"))
	if idx < 0 {
		return 0, malformed("missing startxref")
	}
	p := parser{data: tail, pos: idx + len("startxref")}
	p.skipWhitespace()
	tok, ok := p.token()
	if !ok {
		return 0, malformed("bad startxref")
	}
	n, err := strconv.ParseInt(tok, 10, 64)
	if err != nil || n < 0 {
		return 0, malformed("bad startxref offset")
	}
	return n, nil
}

func (d *Document) loadXRefChain(offset int64) error {
	seen := map[int64]bool{}
	for offset > 0 && offset < int64(len(d.data)) && !seen[offset] {
		seen[offset] = true
		next, trailer, err := d.loadXRefSection(offset)
		if err != nil {
			return err
		}
		if d.trailer == nil {
			d.trailer = trailer
		} else {
			for k, v := range trailer {
				if _, exists := d.trailer[k]; !exists {
					d.trailer[k] = v
				}
			}
		}
		offset = next
	}
	if d.trailer == nil {
		return malformed("no trailer found")
	}
	return nil
}

func (d *Document) loadXRefSection(offset int64) (int64, map[string]any, error) {
	p := parser{data: d.data, pos: int(offset)}
	tok, ok := p.token()
	if !ok {
		return 0, nil, malformed("bad xref section")
	}
	if tok == "xref" {
		return d.loadClassicXRef(&p)
	}
	p.pos = int(offset)
	num, gen, dict, stm, _, err := p.indirectObject()
	if err != nil {
		return 0, nil, err
	}
	_ = num
	_ = gen
	if dict == nil || dict["Type"] != Name("XRef") || stm == nil {
		return 0, nil, malformed("unsupported xref section")
	}
	return d.loadXRefStream(stm, dict)
}

func (d *Document) loadClassicXRef(p *parser) (int64, map[string]any, error) {
	var trailer map[string]any
	for {
		p.skipWhitespace()
		if p.pos >= len(p.data) {
			break
		}
		if p.pos+7 <= len(p.data) && bytes.HasPrefix(p.data[p.pos:], []byte("trailer")) {
			p.pos += 7
			obj, err := p.object()
			if err != nil {
				return 0, nil, err
			}
			td, ok := obj.(map[string]any)
			if !ok {
				return 0, nil, malformed("bad trailer")
			}
			trailer = td
			break
		}
		startTok, ok := p.token()
		if !ok {
			return 0, nil, malformed("bad xref subsection")
		}
		start, err := strconv.Atoi(startTok)
		if err != nil {
			return 0, nil, malformed("bad xref subsection start")
		}
		countTok, ok := p.token()
		if !ok {
			return 0, nil, malformed("bad xref subsection count")
		}
		count, err := strconv.Atoi(countTok)
		if err != nil {
			return 0, nil, malformed("bad xref subsection count")
		}
		for i := 0; i < count; i++ {
			offTok, ok := p.token()
			if !ok {
				return 0, nil, malformed("truncated xref table")
			}
			off, err := strconv.ParseInt(offTok, 10, 64)
			if err != nil {
				return 0, nil, malformed("bad xref entry offset")
			}
			if _, ok := p.token(); !ok {
				return 0, nil, malformed("truncated xref table")
			}
			kindTok, ok := p.token()
			if !ok || len(kindTok) == 0 {
				return 0, nil, malformed("truncated xref table")
			}
			kind := kindTok[0]
			objNum := start + i
			if _, exists := d.xref[objNum]; exists && d.xref[objNum].kind == 'n' {
				continue
			}
			d.xref[objNum] = xrefEntry{offset: off, kind: kind}
		}
	}
	if trailer == nil {
		return 0, nil, malformed("missing trailer")
	}
	prev := int64(0)
	if pv, ok := trailer["Prev"].(int64); ok {
		prev = pv
	}
	return prev, trailer, nil
}

func (d *Document) loadXRefStream(stm *Stream, dict map[string]any) (int64, map[string]any, error) {
	data, err := d.decodeStream(stm, dict)
	if err != nil {
		return 0, nil, err
	}
	wArr, ok := dict["W"].([]any)
	if !ok || len(wArr) < 3 {
		return 0, nil, malformed("xref stream missing W array")
	}
	widths := make([]int, 3)
	for i := 0; i < 3; i++ {
		if n, ok := wArr[i].(int64); ok {
			widths[i] = int(n)
		}
	}
	var index []int
	if arr, ok := dict["Index"].([]any); ok {
		for _, v := range arr {
			if n, ok := v.(int64); ok {
				index = append(index, int(n))
			}
		}
	}
	if len(index) == 0 {
		index = []int{0, len(data) / max(1, widths[0]+widths[1]+widths[2])}
	}
	pos := 0
	readField := func(width int) int64 {
		var v int64
		for k := 0; k < width; k++ {
			v = v<<8 | int64(data[pos+k])
		}
		pos += width
		return v
	}
	for pair := 0; pair+1 < len(index); pair += 2 {
		start, count := index[pair], index[pair+1]
		for i := 0; i < count; i++ {
			if pos+widths[0]+widths[1]+widths[2] > len(data) {
				return 0, nil, malformed("truncated xref stream")
			}
			t := readField(widths[0])
			f2 := readField(widths[1])
			f3 := readField(widths[2])
			objNum := start + i
			entry := xrefEntry{}
			switch t {
			case 0:
				entry.kind = 'f'
			case 1:
				entry.kind = 'n'
				entry.offset = f2
			case 2:
				entry.kind = 'n'
				entry.objStream = int(f2)
				entry.objIndex = int(f3)
			default:
				continue
			}
			if existing, exists := d.xref[objNum]; exists && existing.kind == 'n' {
				continue
			}
			d.xref[objNum] = entry
		}
	}
	prev := int64(0)
	if pv, ok := dict["Prev"].(int64); ok {
		prev = pv
	}
	return prev, dict, nil
}

func (d *Document) GetObject(num int) (any, error) {
	if obj, ok := d.objs[num]; ok {
		return obj, nil
	}
	entry, ok := d.xref[num]
	if !ok || entry.kind != 'n' {
		return nil, malformed("object " + strconv.Itoa(num) + " not found")
	}
	var obj any
	if entry.objStream > 0 {
		stmObj, err := d.GetObject(entry.objStream)
		if err != nil {
			return nil, err
		}
		stm, ok := stmObj.(*Stream)
		if !ok {
			return nil, malformed("object stream " + strconv.Itoa(entry.objStream) + " not a stream")
		}
		obj, err = d.objectFromStream(stm, entry.objIndex)
		if err != nil {
			return nil, err
		}
	} else {
		p := parser{data: d.data, pos: int(entry.offset)}
		num2, _, _, _, body, err := p.indirectObject()
		if err != nil {
			return nil, err
		}
		if num2 != num {
			return nil, malformed("xref offset mismatch for object " + strconv.Itoa(num))
		}
		obj = body
	}
	d.objs[num] = obj
	return obj, nil
}

func (d *Document) objectFromStream(stm *Stream, index int) (any, error) {
	dict := stm.Dict
	data, err := d.decodeStream(stm, dict)
	if err != nil {
		return nil, err
	}
	n, ok := dict["N"].(int64)
	if !ok {
		return nil, malformed("object stream missing N")
	}
	first, ok := dict["First"].(int64)
	if !ok {
		return nil, malformed("object stream missing First")
	}
	p := parser{data: data, pos: 0}
	type pair struct{ num, offset int }
	pairs := make([]pair, 0, n)
	for i := int64(0); i < n; i++ {
		numTok, ok := p.token()
		if !ok {
			return nil, malformed("truncated object stream header")
		}
		num, err := strconv.Atoi(numTok)
		if err != nil {
			return nil, malformed("bad object stream entry")
		}
		offTok, ok := p.token()
		if !ok {
			return nil, malformed("truncated object stream header")
		}
		off, err := strconv.Atoi(offTok)
		if err != nil {
			return nil, malformed("bad object stream entry")
		}
		pairs = append(pairs, pair{num, off})
	}
	if index >= len(pairs) {
		return nil, malformed("object stream index out of range")
	}
	op := parser{data: data, pos: int(first) + pairs[index].offset}
	return op.object()
}

func (d *Document) Resolve(obj any) any {
	for i := 0; i < 32; i++ {
		ref, ok := obj.(Ref)
		if !ok {
			return obj
		}
		resolved, err := d.GetObject(ref.Num)
		if err != nil {
			return nil
		}
		obj = resolved
	}
	return nil
}

func (d *Document) Trailer() map[string]any { return d.trailer }

func (d *Document) buildPageTree() error {
	catalog, ok := d.Resolve(d.trailer["Root"]).(map[string]any)
	if !ok {
		return malformed("bad document catalog")
	}
	pagesRef, ok := catalog["Pages"].(Ref)
	if !ok {
		return malformed("catalog missing Pages")
	}
	return d.walkPages(pagesRef, nil)
}

func (d *Document) walkPages(node Ref, parentChain []Ref) error {
	obj, err := d.GetObject(node.Num)
	if err != nil {
		return err
	}
	dict, ok := obj.(map[string]any)
	if !ok {
		return malformed("bad page tree node")
	}
	d.pageDict[node.Num] = dict
	chain := append(append([]Ref{}, parentChain...), node)
	switch dict["Type"] {
	case Name("Page"):
		d.pages = append(d.pages, node)
		return nil
	case Name("Pages"):
		kids, ok := d.Resolve(dict["Kids"]).([]any)
		if !ok {
			return nil
		}
		for _, kid := range kids {
			if kidRef, ok := kid.(Ref); ok {
				if err := d.walkPages(kidRef, chain); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if _, hasKids := dict["Kids"]; hasKids {
		kids, _ := d.Resolve(dict["Kids"]).([]any)
		for _, kid := range kids {
			if kidRef, ok := kid.(Ref); ok {
				if err := d.walkPages(kidRef, chain); err != nil {
					return err
				}
			}
		}
		return nil
	}
	d.pages = append(d.pages, node)
	return nil
}

func (d *Document) Pages() []Ref { return d.pages }

func (d *Document) PageDict(r Ref) map[string]any { return d.pageDict[r.Num] }

func (d *Document) PageContents(r Ref) []Ref {
	dict := d.pageDict[r.Num]
	if dict == nil {
		return nil
	}
	var out []Ref
	switch c := dict["Contents"].(type) {
	case Ref:
		out = append(out, c)
	case []any:
		for _, item := range c {
			if ref, ok := item.(Ref); ok {
				out = append(out, ref)
			}
		}
	}
	return out
}

func (d *Document) OwnPageResources(r Ref) []map[string]any {
	dict := d.pageDict[r.Num]
	if dict == nil {
		return nil
	}
	var out []map[string]any
	if res, ok := dict["Resources"].(map[string]any); ok {
		out = append(out, res)
	} else if resRef, ok := dict["Resources"].(Ref); ok {
		if resObj, err := d.GetObject(resRef.Num); err == nil {
			if res, ok := resObj.(map[string]any); ok {
				out = append(out, res)
			}
		}
	}
	return out
}

func (d *Document) PageResources(r Ref) []map[string]any {
	chain := d.ancestorChain(r)
	var out []map[string]any
	for _, nodeRef := range chain {
		dict := d.pageDict[nodeRef.Num]
		if dict == nil {
			continue
		}
		if res, ok := dict["Resources"].(map[string]any); ok {
			out = append(out, res)
		} else if resRef, ok := dict["Resources"].(Ref); ok {
			if resObj, err := d.GetObject(resRef.Num); err == nil {
				if res, ok := resObj.(map[string]any); ok {
					out = append(out, res)
				}
			}
		}
	}
	return out
}

func (d *Document) ancestorChain(r Ref) []Ref {
	var chain []Ref
	seen := map[int]bool{}
	cur := r
	for {
		if seen[cur.Num] {
			break
		}
		seen[cur.Num] = true
		chain = append(chain, cur)
		dict := d.pageDict[cur.Num]
		if dict == nil {
			break
		}
		parent, ok := dict["Parent"].(Ref)
		if !ok {
			break
		}
		cur = parent
	}
	return chain
}

// StreamData decodes a stream once per document; callers share the returned
// bytes and must not modify them.
func (d *Document) StreamData(obj any) ([]byte, bool) {
	s, ok := obj.(*Stream)
	if !ok {
		return nil, false
	}
	if cached, ok := d.streams[s]; ok {
		return cached.data, cached.ok
	}
	decoded := decodedStream{data: s.Raw}
	if data, err := d.decodeStream(s, s.Dict); err == nil {
		decoded = decodedStream{data: data, ok: true}
	}
	d.streams[s] = decoded
	return decoded.data, decoded.ok
}
