package extract

import (
	"testing"

	"github.com/m7medVision/docstomd-go/internal/pdf/internal/fuzzguard"
	"github.com/m7medVision/docstomd-go/internal/pdf/parse"
)

// FuzzToUnicodeCMap parses arbitrary ToUnicode CMap text and checks the
// entry cap holds.
func FuzzToUnicodeCMap(f *testing.F) {
	f.Add([]byte("1 beginbfrange\n<0001> <0003> <0061>\nendbfrange\n1 beginbfchar\n<0005> <D835DC00>\nendbfchar\n"))
	f.Add(hostileToUnicode())
	f.Add([]byte("1 beginbfrange\n<80000000000000000000> <7FFFFFFFFFFFFFFFFFFF> <0041>\nendbfrange\n"))
	guard := fuzzguard.Start(f, fuzzHeapBudget, fuzzTimeBudget)
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > fuzzMaxContent {
			t.Skip("input larger than fuzz cap")
		}
		guard.Begin()
		defer guard.End()
		if n := len(parseToUnicodeCMap(data).entries); n > maxFontTableEntries {
			t.Fatalf("entries = %d, cap %d", n, maxFontTableEntries)
		}
	})
}

// FuzzSFNTCmap reads the cmap of an arbitrary sfnt and checks the entry cap
// holds.
func FuzzSFNTCmap(f *testing.F) {
	f.Add(buildSFNT())
	f.Add(hostileFormat4())
	f.Add(hostileFormat12())
	guard := fuzzguard.Start(f, fuzzHeapBudget, fuzzTimeBudget)
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > fuzzMaxContent {
			t.Skip("input larger than fuzz cap")
		}
		guard.Begin()
		defer guard.End()
		if n := len(sfntGIDToUnicode(data)); n > maxFontTableEntries {
			t.Fatalf("entries = %d, cap %d", n, maxFontTableEntries)
		}
	})
}

// FuzzCIDWidths parses arbitrary text as the body of a CIDFont /W array and
// checks that only CIDs in 0..maxCID are kept.
func FuzzCIDWidths(f *testing.F) {
	f.Add("1 [100 200] 5 7 300")
	f.Add("0 400000000 500")
	f.Add("-5 3 100 65534 [1 2 3 4]")
	f.Add(hostileW())
	guard := fuzzguard.Start(f, fuzzHeapBudget, fuzzTimeBudget)
	f.Fuzz(func(t *testing.T, w string) {
		if len(w) > fuzzMaxContent {
			t.Skip("input larger than fuzz cap")
		}
		guard.Begin()
		defer guard.End()
		doc, err := parse.Parse(buildCIDFontPDF(w, nil, nil, ""))
		if err != nil {
			return
		}
		cidObj, err := doc.GetObject(5)
		if err != nil {
			return
		}
		cidDict, ok := cidObj.(map[string]any)
		if !ok {
			return
		}
		wArr, ok := doc.Resolve(cidDict["W"]).([]any)
		if !ok {
			return
		}
		widths := map[int]uint16{}
		parseCIDWArray(doc, wArr, widths)
		for cid := range widths {
			if cid < 0 || cid > maxCID {
				t.Fatalf("kept CID %d outside 0..%d", cid, maxCID)
			}
		}
	})
}
