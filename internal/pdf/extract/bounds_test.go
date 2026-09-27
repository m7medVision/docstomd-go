package extract

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/m7medVision/docstomd-go/internal/pdf/parse"
)

// Budgets a crafted font must convert within. A bounded font table costs a
// few tens of MiB and well under a second; before the caps each case below
// ran for tens of seconds or exhausted memory.
const (
	fontCaseTimeBudget  = 5 * time.Second
	fontCaseAllocBudget = 128 << 20
)

// buildCIDFontPDF assembles a one-page PDF whose only font, F1, is a Type0
// Identity-H font. w is the body of the CIDFont's /W array, toUnicode (when
// non-nil) is its ToUnicode CMap, and sfnt (when non-nil) is its embedded
// FontFile2.
func buildCIDFontPDF(w string, toUnicode, sfnt []byte, content string) []byte {
	var buf bytes.Buffer
	offsets := map[int]int{}
	obj := func(num int, body string) {
		offsets[num] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", num, body)
	}
	stream := func(num int, data []byte) {
		offsets[num] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n<< /Length %d >>\nstream\n", num, len(data))
		buf.Write(data)
		buf.WriteString("\nendstream\nendobj\n")
	}
	font := "<< /Type /Font /Subtype /Type0 /BaseFont /Fake /Encoding /Identity-H /DescendantFonts [5 0 R]"
	if toUnicode != nil {
		font += " /ToUnicode 8 0 R"
	}
	font += " >>"
	desc := "<< /Type /FontDescriptor /FontName /Fake /Flags 4"
	if sfnt != nil {
		desc += " /FontFile2 7 0 R"
	}
	desc += " >>"

	buf.WriteString("%PDF-1.4\n")
	obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	obj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	obj(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 9 0 R /Resources << /Font << /F1 4 0 R >> >> >>")
	obj(4, font)
	obj(5, "<< /Type /Font /Subtype /CIDFontType2 /BaseFont /Fake /DW 1000 /W ["+w+"] /FontDescriptor 6 0 R >>")
	obj(6, desc)
	stream(7, sfnt)
	stream(8, toUnicode)
	stream(9, []byte(content))
	xrefOff := buf.Len()
	buf.WriteString("xref\n0 10\n0000000000 65535 f \n")
	for i := 1; i <= 9; i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer << /Size 10 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xrefOff)
	return buf.Bytes()
}

// buildSFNTCmap returns an sfnt whose only table is a cmap with one
// Windows Unicode subtable, sub.
func buildSFNTCmap(sub []byte) []byte {
	b := make([]byte, 0, 40+len(sub))
	b = append(b, 0x00, 0x01, 0x00, 0x00)             // sfntVersion 1.0
	b = append(b, 0x00, 0x01)                         // numTables = 1
	b = append(b, 0x00, 0x10, 0x00, 0x00, 0x00, 0x00) // searchRange, entrySelector, rangeShift
	b = append(b, 'c', 'm', 'a', 'p')                 // tag
	b = append(b, 0, 0, 0, 0)                         // checksum (unchecked)
	b = append(b, 0, 0, 0, 28)                        // table offset
	b = binary.BigEndian.AppendUint32(b, uint32(12+len(sub)))
	b = append(b, 0x00, 0x00)             // cmap version 0
	b = append(b, 0x00, 0x01)             // cmap numTables 1
	b = append(b, 0x00, 0x03, 0x00, 0x0a) // platform 3 (Windows), encoding 10 (Unicode full)
	b = append(b, 0, 0, 0, 12)            // subtable offset, relative to cmap start
	return append(b, sub...)
}

// cmapFormat4 builds a format 4 subtable from parallel segment arrays; every
// idRangeOffset is 0.
func cmapFormat4(starts, ends []uint16, deltas []int16) []byte {
	n := len(starts)
	b := binary.BigEndian.AppendUint16(nil, 4)
	b = binary.BigEndian.AppendUint16(b, uint16(16+n*8)) // length (unchecked)
	b = binary.BigEndian.AppendUint16(b, 0)              // language
	b = binary.BigEndian.AppendUint16(b, uint16(n*2))    // segCountX2
	b = append(b, 0, 0, 0, 0, 0, 0)                      // searchRange, entrySelector, rangeShift
	for _, e := range ends {
		b = binary.BigEndian.AppendUint16(b, e)
	}
	b = append(b, 0, 0) // reservedPad
	for _, s := range starts {
		b = binary.BigEndian.AppendUint16(b, s)
	}
	for _, d := range deltas {
		b = binary.BigEndian.AppendUint16(b, uint16(d))
	}
	for range n {
		b = append(b, 0, 0)
	}
	return b
}

// cmapFormat12 builds a format 12 subtable from {start, end, startGID}
// groups.
func cmapFormat12(groups [][3]uint32) []byte {
	b := binary.BigEndian.AppendUint16(nil, 12)
	b = append(b, 0, 0) // reserved
	b = binary.BigEndian.AppendUint32(b, uint32(16+len(groups)*12))
	b = binary.BigEndian.AppendUint32(b, 0) // language
	b = binary.BigEndian.AppendUint32(b, uint32(len(groups)))
	for _, g := range groups {
		b = binary.BigEndian.AppendUint32(b, g[0])
		b = binary.BigEndian.AppendUint32(b, g[1])
		b = binary.BigEndian.AppendUint32(b, g[2])
	}
	return b
}

// hostileW is a /W array of 2,000 full-range entries, each naming every CID.
func hostileW() string {
	return "0 400000000 500 " + strings.Repeat("0 65535 500 ", 2000)
}

// hostileToUnicode maps <0001>..<0003> to "abc", then adds 1,000 full-width
// bfranges.
func hostileToUnicode() []byte {
	var sb strings.Builder
	sb.WriteString("1 begincodespacerange\n<0000> <FFFF>\nendcodespacerange\n")
	sb.WriteString("1 beginbfrange\n<0001> <0003> <0061>\nendbfrange\n")
	sb.WriteString("1000 beginbfrange\n")
	for range 1000 {
		sb.WriteString("<0100> <FFFF> <4E00>\n")
	}
	sb.WriteString("endbfrange\n")
	return []byte(sb.String())
}

// hostileFormat4 maps code 'A' to GID 1, then adds 4,000 segments that each
// cover codes 0x0100..0xFFFE.
func hostileFormat4() []byte {
	starts := []uint16{0x41}
	ends := []uint16{0x41}
	deltas := []int16{1 - 0x41}
	for range 4000 {
		starts = append(starts, 0x0100)
		ends = append(ends, 0xFFFE)
		deltas = append(deltas, 0)
	}
	return buildSFNTCmap(cmapFormat4(starts, ends, deltas))
}

// hostileFormat12 maps code 'A' to GID 1, then adds 3,000 groups of 65,536
// codes, each onto its own GID range, so every group adds new map keys.
func hostileFormat12() []byte {
	groups := [][3]uint32{{0x41, 0x41, 1}}
	for g := range uint32(3000) {
		groups = append(groups, [3]uint32{0x10000, 0x1FFFF, (g + 1) << 16})
	}
	return buildSFNTCmap(cmapFormat12(groups))
}

func TestCraftedFontTablesAreBounded(t *testing.T) {
	tests := []struct {
		name      string
		w         string
		toUnicode []byte
		sfnt      []byte
		content   string
		want      string
	}{
		{
			name:    "CID W ranges",
			w:       hostileW(),
			content: "BT /F1 12 Tf 72 700 Td <0041> Tj ET",
		},
		{
			name:      "ToUnicode bfranges",
			toUnicode: hostileToUnicode(),
			content:   "BT /F1 12 Tf 72 700 Td <000100020003> Tj ET",
			want:      "abc",
		},
		{
			name:    "sfnt cmap format 4",
			sfnt:    hostileFormat4(),
			content: "BT /F1 12 Tf 72 700 Td <0001> Tj ET",
			want:    "A",
		},
		{
			name:    "sfnt cmap format 12",
			sfnt:    hostileFormat12(),
			content: "BT /F1 12 Tf 72 700 Td <0001> Tj ET",
			want:    "A",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := parse.Parse(buildCIDFontPDF(tt.w, tt.toUnicode, tt.sfnt, tt.content))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			start := time.Now()
			pages := Extract(doc)
			elapsed := time.Since(start)
			runtime.ReadMemStats(&after)

			if elapsed > fontCaseTimeBudget {
				t.Errorf("Extract took %v, budget %v", elapsed, fontCaseTimeBudget)
			}
			if alloc := after.TotalAlloc - before.TotalAlloc; alloc > fontCaseAllocBudget {
				t.Errorf("Extract allocated %d bytes, budget %d", alloc, fontCaseAllocBudget)
			}
			if len(pages) != 1 {
				t.Fatalf("pages = %d, want 1", len(pages))
			}
			if tt.want == "" {
				return
			}
			var texts []string
			for _, item := range pages[0].Items {
				texts = append(texts, item.Text)
			}
			if got := strings.Join(texts, ""); got != tt.want {
				t.Errorf("text = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseCIDWArray(t *testing.T) {
	tests := []struct {
		name string
		w    string
		want map[int]uint16
	}{
		{
			name: "both forms",
			w:    "1 [100 200] 5 7 300",
			want: map[int]uint16{1: 100, 2: 200, 5: 300, 6: 300, 7: 300},
		},
		{
			name: "range past the last CID is dropped",
			w:    "0 400000000 500 9 9 90",
			want: map[int]uint16{9: 90},
		},
		{
			name: "negative and reversed ranges are dropped",
			w:    "-5 3 100 8 4 100 2 2 20",
			want: map[int]uint16{2: 20},
		},
		{
			name: "array form stops at the last CID",
			w:    "65534 [1 2 3 4]",
			want: map[int]uint16{65534: 1, 65535: 2},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := parse.Parse(buildCIDFontPDF(tt.w, nil, nil, ""))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			cidDict, err := doc.GetObject(5)
			if err != nil {
				t.Fatalf("GetObject(5): %v", err)
			}
			wArr, ok := doc.Resolve(cidDict.(map[string]any)["W"]).([]any)
			if !ok {
				t.Fatal("CIDFont /W is not an array")
			}
			got := map[int]uint16{}
			parseCIDWArray(doc, wArr, got)
			if len(got) != len(tt.want) {
				t.Errorf("widths = %v, want %v", got, tt.want)
				return
			}
			for cid, w := range tt.want {
				if got[cid] != w {
					t.Errorf("widths = %v, want %v", got, tt.want)
					return
				}
			}
		})
	}
}

func TestFontTableCaps(t *testing.T) {
	if n := len(parseToUnicodeCMap(hostileToUnicode()).entries); n > maxFontTableEntries {
		t.Errorf("ToUnicode entries = %d, cap %d", n, maxFontTableEntries)
	}
	if n := len(sfntGIDToUnicode(hostileFormat12())); n > maxFontTableEntries {
		t.Errorf("format 12 entries = %d, cap %d", n, maxFontTableEntries)
	}
	// Codes longer than four bytes would overflow codeOf and slip past the
	// range-width check.
	long := "1 beginbfrange\n<80000000000000000000> <7FFFFFFFFFFFFFFFFFFF> <0041>\nendbfrange\n"
	if n := len(parseToUnicodeCMap([]byte(long)).entries); n != 0 {
		t.Errorf("over-long bfrange added %d entries, want 0", n)
	}
}
