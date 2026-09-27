package parse

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/m7medVision/docstomd-go/internal/pdftest"
)

func requireMalformed(t *testing.T, err error, wantDetail string) {
	t.Helper()
	var malformedErr *MalformedError
	if !errors.As(err, &malformedErr) {
		t.Fatalf("err = %v, want *MalformedError", err)
	}
	if !strings.Contains(malformedErr.Detail, wantDetail) {
		t.Errorf("detail = %q, want it to contain %q", malformedErr.Detail, wantDetail)
	}
}

func TestParseRejectsHostileStructure(t *testing.T) {
	tests := []struct {
		name       string
		data       []byte
		wantDetail string
	}{
		{"page tree cycle", pdftest.PageTreeCycle(), "is its own ancestor"},
		{"xref W negative", pdftest.XRefWidths("[1 -2 1]"), "W entry out of range"},
		{"xref W too wide", pdftest.XRefWidths("[1 9 1]"), "W entry out of range"},
		{"xref W all zero", pdftest.XRefWidths("[0 0 0]"), "W entries are all zero"},
		{"xref predictor negative colors", pdftest.XRefPredictor(-8), "Colors out of range"},
		{"page number +Inf", pdftest.NonFiniteNumber("+Inf"), "bad number"},
		{"page number -Infinity", pdftest.NonFiniteNumber("-Infinity"), "bad number"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.data)
			requireMalformed(t, err, tt.wantDetail)
		})
	}
}

func TestParseRejectsHostileObjects(t *testing.T) {
	tests := []struct {
		name       string
		data       []byte
		num        int
		wantDetail string
	}{
		{"object stream inside itself", pdftest.ObjectStreamCycle(10), 1, "inside another object stream"},
		{"object streams inside each other", pdftest.ObjectStreamCycle(11), 1, "inside another object stream"},
		{"object stream negative N", pdftest.ObjectStreamCount("-1"), 1, "N out of range"},
		{"object stream huge N", pdftest.ObjectStreamCount("1099511627776"), 1, "N out of range"},
		{"deep arrays", pdftest.DeepNesting(100_000, "["), 1, "nested deeper than 64"},
		{"deep dictionaries", pdftest.DeepNesting(100_000, "<<"), 1, "nested deeper than 64"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// These documents fail in Parse too; build the xref alone so the
			// object lookup's own error is what the test sees.
			d := &Document{data: tt.data, xref: map[int]xrefEntry{}, objs: map[int]any{}, streams: map[*Stream]decodedStream{}}
			offset, err := d.startXRef()
			if err != nil {
				t.Fatalf("startxref: %v", err)
			}
			if err := d.loadXRefChain(offset); err != nil {
				t.Fatalf("load xref: %v", err)
			}
			_, err = d.GetObject(tt.num)
			requireMalformed(t, err, tt.wantDetail)
			if _, err := Parse(tt.data); err == nil {
				t.Error("Parse succeeded, want an error")
			}
		})
	}
}

func TestClassicXRefRejectsOffsetOutsideFile(t *testing.T) {
	data := pdftest.NegativeXRefOffset()
	d := &Document{data: data, xref: map[int]xrefEntry{}}
	offset, err := d.startXRef()
	if err != nil {
		t.Fatalf("startxref: %v", err)
	}
	requireMalformed(t, d.loadXRefChain(offset), "outside the file")
	_, err = Parse(data)
	requireMalformed(t, err, "bad document catalog")
}

func TestGetObjectRejectsOffsetOutsideFile(t *testing.T) {
	d := &Document{data: []byte("%PDF-1.7\n"), xref: map[int]xrefEntry{1: {offset: -5, kind: 'n'}, 2: {offset: 1 << 40, kind: 'n'}}, objs: map[int]any{}}
	for _, num := range []int{1, 2} {
		_, err := d.GetObject(num)
		requireMalformed(t, err, "offset outside the file")
	}
}

func TestParserNestingLimit(t *testing.T) {
	tests := []struct {
		name    string
		depth   int
		wantErr bool
	}{
		{"at the limit", maxObjectDepth, false},
		{"one past the limit", maxObjectDepth + 1, true},
	}
	for _, tt := range tests {
		for _, open := range []string{"[", "<<"} {
			t.Run(tt.name+" "+open, func(t *testing.T) {
				src := strings.Repeat("[", tt.depth) + strings.Repeat("]", tt.depth)
				if open == "<<" {
					src = strings.Repeat("<< /A ", tt.depth) + "0" + strings.Repeat(" >>", tt.depth)
				}
				p := parser{data: []byte(src)}
				_, err := p.object()
				if tt.wantErr {
					requireMalformed(t, err, "nested deeper")
					return
				}
				if err != nil {
					t.Fatalf("object: %v", err)
				}
				if p.depth != 0 {
					t.Errorf("depth after parse = %d, want 0", p.depth)
				}
			})
		}
	}
}

func TestNumberRejectsNonFinite(t *testing.T) {
	for _, tok := range []string{"+Inf", "-Inf", "+Infinity", "-infinity", "1e400"} {
		p := parser{data: []byte(tok)}
		if got, err := p.number(); err == nil {
			t.Errorf("number(%q) = %v, want an error", tok, got)
		}
	}
	p := parser{data: []byte("-1.5")}
	got, err := p.number()
	if err != nil || got != -1.5 {
		t.Errorf("number(-1.5) = %v, %v", got, err)
	}
}

func TestParseXRefStreamWithoutTypeField(t *testing.T) {
	doc, err := Parse(pdftest.XRefWithoutType())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := len(doc.Pages()); got != 1 {
		t.Fatalf("pages = %d, want 1", got)
	}
	refs := doc.PageContents(doc.Pages()[0])
	if len(refs) != 1 {
		t.Fatalf("content refs = %v, want one", refs)
	}
	obj, err := doc.GetObject(refs[0].Num)
	if err != nil {
		t.Fatalf("content object: %v", err)
	}
	content, ok := doc.StreamData(obj)
	if !ok || !bytes.Contains(content, []byte("(typeless)")) {
		t.Errorf("content = %q, want the page text", content)
	}
}

func TestPageTreeSkipsSharedSubtree(t *testing.T) {
	page := pdftest.TextPage("shared")
	page[1].Body = "<< /Type /Pages /Kids [3 0 R 3 0 R] /Count 2 >>"
	doc, err := Parse(pdftest.Classic(page))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got, want := doc.Pages(), []Ref{{Num: 3}}; !reflect.DeepEqual(got, want) {
		t.Errorf("pages = %v, want %v", got, want)
	}
}

func TestFlateDecodeLimit(t *testing.T) {
	data := pdftest.FlateBomb(flateMinLimit + 1)
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	obj, err := doc.GetObject(5)
	if err != nil {
		t.Fatalf("content object: %v", err)
	}
	stm := obj.(*Stream)
	_, err = doc.decodeStream(stm, stm.Dict)
	requireMalformed(t, err, "-byte limit")

	atLimit := pdftest.FlateBomb(flateMinLimit)
	doc, err = Parse(atLimit)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	obj, err = doc.GetObject(5)
	if err != nil {
		t.Fatalf("content object: %v", err)
	}
	stm = obj.(*Stream)
	out, err := doc.decodeStream(stm, stm.Dict)
	if err != nil || len(out) != flateMinLimit {
		t.Errorf("decode at the limit: %d bytes, %v; want %d bytes", len(out), err, flateMinLimit)
	}
}

func TestFlateLimitScalesWithFileSize(t *testing.T) {
	tests := []struct {
		fileSize int
		want     int64
	}{
		{0, flateMinLimit},
		{1 << 10, flateMinLimit},
		{1 << 20, 100 << 20},
		{3 << 20, flateMaxLimit},
	}
	for _, tt := range tests {
		if got := flateLimit(tt.fileSize); got != tt.want {
			t.Errorf("flateLimit(%d) = %d, want %d", tt.fileSize, got, tt.want)
		}
	}
}

// pngEncode applies PNG filter type to each row of raw, the inverse of
// applyPredictor, taking prior rows' raw bytes as "up".
func pngEncode(raw []byte, rowLen, bpp int, types []byte) []byte {
	var out []byte
	prev := make([]byte, rowLen)
	for r := 0; r*rowLen < len(raw); r++ {
		row := raw[r*rowLen : (r+1)*rowLen]
		filter := types[r%len(types)]
		out = append(out, filter)
		for i := range row {
			var left, upLeft byte
			if i >= bpp {
				left = row[i-bpp]
				upLeft = prev[i-bpp]
			}
			up := prev[i]
			var pred byte
			switch filter {
			case 1:
				pred = left
			case 2:
				pred = up
			case 3:
				pred = byte((int(left) + int(up)) / 2)
			case 4:
				pred = paeth(left, up, upLeft)
			}
			out = append(out, row[i]-pred)
		}
		prev = row
	}
	return out
}

func TestApplyPredictorDecodesEveryPNGPredictor(t *testing.T) {
	const colors, columns = 3, 5
	rowLen := colors * columns
	raw := make([]byte, rowLen*6)
	for i := range raw {
		raw[i] = byte(i*37 + i*i)
	}
	// Predictors 10-14 name the filter the encoder used; 15 lets it pick
	// per row. The decoder reads every row's own tag either way.
	tests := []struct {
		predictor int64
		types     []byte
	}{
		{10, []byte{0}},
		{11, []byte{1}},
		{12, []byte{2}},
		{13, []byte{3}},
		{14, []byte{4}},
		{15, []byte{0, 1, 2, 3, 4, 4}},
	}
	for _, tt := range tests {
		encoded := pngEncode(raw, rowLen, colors, tt.types)
		parm := map[string]any{"Predictor": tt.predictor, "Colors": int64(colors), "Columns": int64(columns)}
		got, err := applyPredictor(encoded, parm)
		if err != nil {
			t.Fatalf("predictor %d: %v", tt.predictor, err)
		}
		if !bytes.Equal(got, raw) {
			t.Errorf("predictor %d decoded %x, want %x", tt.predictor, got, raw)
		}
	}
}

func TestApplyPredictorRejectsBadParms(t *testing.T) {
	tests := []struct {
		name       string
		parm       map[string]any
		wantDetail string
	}{
		{"negative colors", map[string]any{"Predictor": int64(12), "Colors": int64(-8)}, "Colors out of range"},
		{"zero colors", map[string]any{"Predictor": int64(12), "Colors": int64(0)}, "Colors out of range"},
		{"odd bits", map[string]any{"Predictor": int64(12), "BitsPerComponent": int64(-3)}, "BitsPerComponent out of range"},
		{"zero columns", map[string]any{"Predictor": int64(12), "Columns": int64(0)}, "Columns out of range"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := applyPredictor(make([]byte, 64), tt.parm)
			requireMalformed(t, err, tt.wantDetail)
		})
	}
	got, err := applyPredictor(make([]byte, 64), map[string]any{"Predictor": int64(12), "Columns": int64(1) << 62})
	if err != nil || len(got) != 0 {
		t.Errorf("huge Columns = %d bytes, %v; want no rows", len(got), err)
	}
}

func TestObjectStreamResolvesMembers(t *testing.T) {
	doc, err := Parse(pdftest.ObjectStreamCount("3"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := len(doc.Pages()); got != 1 {
		t.Fatalf("pages = %d, want 1", got)
	}
	page, ok := doc.Resolve(Ref{Num: 3}).(map[string]any)
	if !ok || page["Type"] != Name("Page") {
		t.Errorf("object 3 = %v, want the page dictionary", page)
	}
}

func TestObjectStreamRejectsBadHeader(t *testing.T) {
	tests := []struct {
		name       string
		dict       string
		index      int
		wantDetail string
	}{
		{"missing N", "/N null", 0, "missing N"},
		{"missing First", "/First null", 0, "missing First"},
		{"negative First", "/First -1", 0, "First out of range"},
		{"First past the data", "/First 100000", 0, "First out of range"},
		{"N past the header", "/N 10", 0, "truncated object stream header"},
		{"negative index", "", -1, "index out of range"},
		{"index past N", "", 3, "index out of range"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stm := pdftest.ObjectStream(7, pdftest.TextPage("header")[:3], tt.dict)
			p := parser{data: []byte("<< " + stm.Body + " >>")}
			dict, err := p.object()
			if err != nil {
				t.Fatalf("dict: %v", err)
			}
			d := &Document{}
			_, err = d.objectFromStream(&Stream{Dict: dict.(map[string]any), Raw: stm.Stream}, tt.index)
			requireMalformed(t, err, tt.wantDetail)
		})
	}
}

func TestParserObjectKinds(t *testing.T) {
	tests := []struct {
		src  string
		want any
	}{
		{"true", true},
		{"false", false},
		{"null", nil},
		{"/Name", Name("Name")},
		{"-2.5", -2.5},
		{"12 0 R", Ref{Num: 12}},
		{"12 0 obj", int64(12)},
		{"[1 /A]", []any{int64(1), Name("A")}},
		{"<< /K 1 >>", map[string]any{"K": int64(1)}},
		{"<4142>", []byte("AB")},
	}
	for _, tt := range tests {
		p := parser{data: []byte(tt.src)}
		got, err := p.object()
		if err != nil {
			t.Errorf("object(%q): %v", tt.src, err)
			continue
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("object(%q) = %#v, want %#v", tt.src, got, tt.want)
		}
	}
	for _, src := range []string{"", "bogus", "[1 2", "<< 1 2 >>", "<< /K"} {
		p := parser{data: []byte(src)}
		if got, err := p.object(); err == nil {
			t.Errorf("object(%q) = %#v, want an error", src, got)
		}
	}
}

func TestClassicXRefRejectsBadTable(t *testing.T) {
	good := string(pdftest.Classic(pdftest.TextPage("table")))
	xrefAt := strings.Index(good, "xref\n")
	tests := []struct {
		name  string
		table string
	}{
		{"bad start", "xref\nx 1\n"},
		{"bad count", "xref\n0 x\n"},
		{"truncated", "xref\n0 2\n0000000000 65535 f \n0000000009"},
		{"bad offset", "xref\n0 1\nabc 00000 n \ntrailer << >>"},
		{"trailer not a dictionary", "xref\n0 0\ntrailer [1]"},
		{"no trailer", "xref\n0 0\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := []byte(good[:xrefAt] + tt.table)
			d := &Document{data: data, xref: map[int]xrefEntry{}}
			p := parser{data: data, pos: xrefAt + len("xref")}
			_, _, err := d.loadClassicXRef(&p)
			var malformedErr *MalformedError
			if !errors.As(err, &malformedErr) {
				t.Errorf("err = %v, want *MalformedError", err)
			}
		})
	}
}
