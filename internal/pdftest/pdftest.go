// Package pdftest builds small PDFs for tests and fuzz seeds, including the
// hostile inputs that once crashed or exhausted the parser.
package pdftest

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"strings"
)

// Object is one indirect object. Body is written between "N 0 obj" and
// "endobj". When Stream is non-nil, Body is the stream dictionary's entries
// without the enclosing "<< >>" or /Length, and Stream is its data.
type Object struct {
	Num    int
	Body   string
	Stream []byte
}

// XRefEntry is one xref stream row: type, field 2 and field 3.
type XRefEntry [3]int64

// XRefStream describes the xref stream that ends a WithXRefStream file.
type XRefStream struct {
	Num int
	// W is the width in bytes of each row field as written. A zero width
	// omits that field.
	W [3]int
	// Dict is appended after the generated /Type, /Size and /W entries, so
	// a key it repeats overrides the generated one.
	Dict string
	// Compress flate-encodes the rows and adds /Filter /FlateDecode.
	Compress bool
	// Rows returns the rows for every object in /Index order, given each
	// object's byte offset.
	Rows func(offsets map[int]int) []XRefEntry
}

func writeObjects(buf *bytes.Buffer, objects []Object) map[int]int {
	buf.WriteString("%PDF-1.7\n")
	offsets := map[int]int{}
	for _, o := range objects {
		offsets[o.Num] = buf.Len()
		if o.Stream == nil {
			fmt.Fprintf(buf, "%d 0 obj\n%s\nendobj\n", o.Num, o.Body)
			continue
		}
		fmt.Fprintf(buf, "%d 0 obj\n<< %s /Length %d >>\nstream\n", o.Num, o.Body, len(o.Stream))
		buf.Write(o.Stream)
		buf.WriteString("\nendstream\nendobj\n")
	}
	return offsets
}

// Classic lays out objects with a classic xref table covering objects 0
// through the highest number and a trailer whose /Root is 1 0 R.
func Classic(objects []Object) []byte {
	var buf bytes.Buffer
	offsets := writeObjects(&buf, objects)
	size := 0
	for _, o := range objects {
		size = max(size, o.Num+1)
	}
	xrefOff := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", size)
	for num := 1; num < size; num++ {
		off, ok := offsets[num]
		if !ok {
			buf.WriteString("0000000000 65535 f \n")
			continue
		}
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", size, xrefOff)
	return buf.Bytes()
}

// WithXRefStream lays out objects and ends with the xref stream x, whose
// /Root is 1 0 R. The file has no "trailer" keyword, so a broken xref stream
// can't be repaired by scanning.
func WithXRefStream(objects []Object, x XRefStream) []byte {
	var buf bytes.Buffer
	offsets := writeObjects(&buf, objects)
	xrefOff := buf.Len()
	offsets[x.Num] = xrefOff
	size := x.Num + 1
	for _, o := range objects {
		size = max(size, o.Num+1)
	}
	var rows bytes.Buffer
	for _, row := range x.Rows(offsets) {
		for i, width := range x.W {
			for k := width - 1; k >= 0; k-- {
				rows.WriteByte(byte(row[i] >> (8 * k)))
			}
		}
	}
	data := rows.Bytes()
	filter := ""
	if x.Compress {
		data = Deflate(data)
		filter = " /Filter /FlateDecode"
	}
	fmt.Fprintf(&buf, "%d 0 obj\n<< /Type /XRef /Size %d /Root 1 0 R /W [%d %d %d]%s %s /Length %d >>\nstream\n",
		x.Num, size, x.W[0], x.W[1], x.W[2], filter, x.Dict, len(data))
	buf.Write(data)
	fmt.Fprintf(&buf, "\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n", xrefOff)
	return buf.Bytes()
}

// ObjectStream packs members into object stream num. dict is appended after
// the generated /Type, /N and /First entries and overrides them.
func ObjectStream(num int, members []Object, dict string) Object {
	var header, bodies strings.Builder
	for _, m := range members {
		fmt.Fprintf(&header, "%d %d ", m.Num, bodies.Len())
		bodies.WriteString(m.Body)
		bodies.WriteString("\n")
	}
	body := fmt.Sprintf("/Type /ObjStm /N %d /First %d %s", len(members), header.Len(), dict)
	return Object{Num: num, Body: body, Stream: []byte(header.String() + bodies.String())}
}

// Deflate zlib-compresses data.
func Deflate(data []byte) []byte {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		panic(err)
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

const helvetica = "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>"

// TextPage returns objects 1-5 of a one-page document: catalog, page tree,
// page, Helvetica as /F1, and content showing text.
func TextPage(text string) []Object {
	content := fmt.Sprintf("BT /F1 12 Tf 72 700 Td (%s) Tj ET", text)
	return []Object{
		{Num: 1, Body: "<< /Type /Catalog /Pages 2 0 R >>"},
		{Num: 2, Body: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>"},
		{Num: 3, Body: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 5 0 R /Resources << /Font << /F1 4 0 R >> >> >>"},
		{Num: 4, Body: helvetica},
		{Num: 5, Stream: []byte(content)},
	}
}

// PageTreeCycle returns a page tree whose second node lists the first as
// its kid.
func PageTreeCycle() []byte {
	return Classic([]Object{
		{Num: 1, Body: "<< /Type /Catalog /Pages 2 0 R >>"},
		{Num: 2, Body: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>"},
		{Num: 3, Body: "<< /Type /Pages /Parent 2 0 R /Kids [2 0 R] /Count 1 >>"},
	})
}

// ObjectStreamCycle puts objects 1-3 in object stream 10 and places 10
// inside object stream container: 10 itself, or 11, which is inside 10.
func ObjectStreamCycle(container int) []byte {
	page := TextPage("cycle")
	objects := []Object{
		page[3],
		page[4],
		ObjectStream(10, page[:3], ""),
		ObjectStream(11, []Object{{Num: 12, Body: "null"}}, ""),
	}
	return WithXRefStream(objects, XRefStream{
		Num: 13,
		W:   [3]int{1, 4, 2},
		Rows: func(offsets map[int]int) []XRefEntry {
			return []XRefEntry{
				{0, 0, 65535},
				{2, 10, 0}, {2, 10, 1}, {2, 10, 2},
				{1, int64(offsets[4]), 0},
				{1, int64(offsets[5]), 0},
				{0, 0, 0}, {0, 0, 0}, {0, 0, 0}, {0, 0, 0},
				{2, int64(container), 0},
				{2, 10, 3},
				{2, 11, 0},
				{1, int64(offsets[13]), 0},
			}
		},
	})
}

// DeepNesting returns a document whose catalog carries depth nested arrays
// (open "[") or dictionaries (open "<<").
func DeepNesting(depth int, open string) []byte {
	nested := strings.Repeat("[", depth) + strings.Repeat("]", depth)
	if open == "<<" {
		nested = strings.Repeat("<< /A ", depth) + "0" + strings.Repeat(" >>", depth)
	}
	page := TextPage("deep")
	page[0].Body = "<< /Type /Catalog /Pages 2 0 R /Deep " + nested + " >>"
	return Classic(page)
}

// FormChain returns a page that draws Form XObject 1, which draws form 2,
// and so on to form depth. Form i shows the text "form<i>".
func FormChain(depth int) []byte {
	objects := TextPage("page")
	objects[2].Body = "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 5 0 R /Resources << /Font << /F1 4 0 R >> /XObject << /Fm1 10 0 R >> >> >>"
	objects[4].Stream = []byte("/Fm1 Do")
	for i := 1; i <= depth; i++ {
		content := fmt.Sprintf("BT /F1 10 Tf 10 %d Td (form%d) Tj ET /Fm%d Do", 10*i, i, i+1)
		dict := fmt.Sprintf("/Type /XObject /Subtype /Form /BBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> /XObject << /Fm%d %d 0 R >> >>", i+1, 10+i)
		objects = append(objects, Object{Num: 9 + i, Body: dict, Stream: []byte(content)})
	}
	return Classic(objects)
}

// QNesting returns a page whose content saves the graphics state n times
// before showing text, then restores it n times.
func QNesting(n int) []byte {
	objects := TextPage("deep")
	objects[4].Stream = []byte(strings.Repeat("q ", n) + "BT /F1 12 Tf 72 700 Td (deep) Tj ET " + strings.Repeat("Q ", n))
	return Classic(objects)
}

// XRefPredictor returns a document whose flate xref stream declares a PNG
// predictor with the given /Colors.
func XRefPredictor(colors int) []byte {
	return WithXRefStream(TextPage("predictor"), XRefStream{
		Num:      6,
		W:        [3]int{1, 4, 1},
		Compress: true,
		Dict:     fmt.Sprintf("/DecodeParms << /Predictor 12 /Colors %d /Columns 6 >>", colors),
		Rows:     inUseRows(6),
	})
}

// XRefWidths returns a document whose xref stream declares /W as w; the
// rows are written with widths 1, 4, 1.
func XRefWidths(w string) []byte {
	return WithXRefStream(TextPage("widths"), XRefStream{
		Num:  6,
		W:    [3]int{1, 4, 1},
		Dict: "/W " + w,
		Rows: inUseRows(6),
	})
}

// XRefWithoutType returns a valid document whose xref stream has /W[0] = 0,
// so every row's type defaults to 1 (in use).
func XRefWithoutType() []byte {
	return WithXRefStream(TextPage("typeless"), XRefStream{
		Num:  6,
		W:    [3]int{0, 4, 1},
		Dict: "/Index [1 6]",
		Rows: func(offsets map[int]int) []XRefEntry {
			var rows []XRefEntry
			for num := 1; num <= 6; num++ {
				rows = append(rows, XRefEntry{0, int64(offsets[num]), 0})
			}
			return rows
		},
	})
}

func inUseRows(last int) func(map[int]int) []XRefEntry {
	return func(offsets map[int]int) []XRefEntry {
		rows := []XRefEntry{{0, 0, 255}}
		for num := 1; num <= last; num++ {
			rows = append(rows, XRefEntry{1, int64(offsets[num]), 0})
		}
		return rows
	}
}

// NegativeXRefOffset returns a classic-xref document whose catalog entry
// has offset -100 and whose catalog object is absent, so scanning for
// objects can't recover it either.
func NegativeXRefOffset() []byte {
	data := Classic(TextPage("negative")[1:])
	free := "0000000000 65535 f \n"
	return bytes.Replace(data, []byte(free+free), []byte(free+"-000000100 00000 n \n"), 1)
}

// ObjectStreamCount returns a document whose page tree lives in an object
// stream declaring /N as n.
func ObjectStreamCount(n string) []byte {
	page := TextPage("count")
	objects := []Object{ObjectStream(7, page[:3], "/N "+n), page[3], page[4]}
	return WithXRefStream(objects, XRefStream{
		Num: 8,
		W:   [3]int{1, 4, 1},
		Rows: func(offsets map[int]int) []XRefEntry {
			return []XRefEntry{
				{0, 0, 255},
				{2, 7, 0}, {2, 7, 1}, {2, 7, 2},
				{1, int64(offsets[4]), 0},
				{1, int64(offsets[5]), 0},
				{0, 0, 0},
				{1, int64(offsets[7]), 0},
				{1, int64(offsets[8]), 0},
			}
		},
	})
}

// NonFiniteNumber returns a document whose page MediaBox holds tok, such as
// "+Inf".
func NonFiniteNumber(tok string) []byte {
	page := TextPage("inf")
	page[2].Body = strings.Replace(page[2].Body, "612", tok, 1)
	return Classic(page)
}

// FlateBomb returns a document whose content stream inflates to size zero
// bytes.
func FlateBomb(size int) []byte {
	page := TextPage("bomb")
	page[4].Body = "/Filter /FlateDecode"
	page[4].Stream = Deflate(make([]byte, size))
	return Classic(page)
}

// Hostile returns every crafted input above at a size that trips its limit,
// for fuzz seeds.
func Hostile() [][]byte {
	return [][]byte{
		PageTreeCycle(),
		ObjectStreamCycle(10),
		ObjectStreamCycle(11),
		DeepNesting(10_000, "["),
		DeepNesting(10_000, "<<"),
		FormChain(40),
		QNesting(1000),
		XRefPredictor(-8),
		XRefWidths("[1 -2 1]"),
		XRefWidths("[0 0 0]"),
		XRefWithoutType(),
		NegativeXRefOffset(),
		ObjectStreamCount("-1"),
		ObjectStreamCount("1099511627776"),
		NonFiniteNumber("+Inf"),
		NonFiniteNumber("-Infinity"),
		FlateBomb(20 << 20),
	}
}
