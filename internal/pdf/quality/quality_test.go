package quality

import (
	"strings"
	"testing"

	"github.com/m7medVision/docstomd-go/internal/pdf/extract"
)

// Item aliases the extraction item for test construction.
type Item = extract.TextItem

func TestDollarAsSpace(t *testing.T) {
	if Analyze("normal text with $100 and $200 prices") {
		t.Error("clean dollar text must not flag")
	}
	garbled := "Th$s $s a br$ken t$xt lay$r wh$re sp$ces we$e re$laced by d$ll$rs ev$rywh$re"
	if !Analyze(garbled) {
		t.Error("dollar-as-space text must flag")
	}
}

func TestReplacementRuns(t *testing.T) {
	if Analyze("The quick brown fox jumps over the lazy dog and keeps jumping repeatedly everywhere") {
		t.Error("clean text must not flag")
	}
	if !Analyze("Short \uFFFD\uFFFD page") {
		t.Error("short page with run >= 2 must flag")
	}
	report := AnalyzeItems([]Item{
		{Page: 1, ItemType: extract.ItemText, Text: "Some longer page with one strange \uFFFD\uFFFD\uFFFD replacement cluster"},
		{Page: 1, ItemType: extract.ItemText, Text: "appearing repeatedly \uFFFD\uFFFD\uFFFD here and there"},
		{Page: 1, ItemType: extract.ItemText, Text: "across \uFFFD\uFFFD\uFFFD the whole body of text"},
	})
	if len(report.PagesNeedingOCR) != 1 || report.PagesNeedingOCR[0] != 1 {
		t.Error("repeated replacement spans across items must flag the page")
	}
	if Analyze("A math formula a\uFFFDb plus c\uFFFDd sits inside a mostly clean and long enough paragraph of ordinary English words that keeps going on and on about nothing at all") {
		t.Error("isolated single replacements in clean text must not flag")
	}
}

func TestPrivateUse(t *testing.T) {
	if !Analyze("\U000F0001\U000F0002\U000F0003 glyph run") {
		t.Error("PUA run >= 3 must flag")
	}
	if !Analyze("x\U000F0001\U000F0002\U000F0003z glyph") {
		t.Error("PUA run of 3 inside a word must flag")
	}
	if Analyze("A normal sentence with words and punctuation.") {
		t.Error("clean text must not flag")
	}
}

func TestC1AndMojibake(t *testing.T) {
	if !Analyze("wo\u0080rd \u0085token\u0081\u0082 control") {
		t.Error("C1 control tokens must flag")
	}
	if !Analyze("\u00EF\u00BD\u008E\u00EF\u00BD\u008F\u00EF\u00BD\u008D high-latin mojibake with almost no ascii") {
		t.Error("high Latin-1 mojibake must flag")
	}
}

func TestGarbageDominance(t *testing.T) {
	if !Analyze("----1-.-.-.___  --.-. .._ I_---. ...--.- ..-.- -.- -. ..-. --. -. -- -.-. ..- -. ..- ..- ...- .-.. .- ... -..-. .-.- .-.. .-.. ...-- .-. ..- .-.. . ... --.. .-. .-.-.") {
		t.Error("non-alphanumeric-dominant text must flag")
	}
	if Analyze("Real sentences with words, commas, and periods in between them all.") {
		t.Error("punctuated normal text must not flag")
	}
}

func TestSpacedDotLeadersAreDecorative(t *testing.T) {
	spaced := "For the Love of Iran. . . . . . . . . . . . . . . . . . . . . . . . . . . . . . . . . . . . . . . . 41"
	if Analyze(spaced) {
		t.Error("table-of-contents line with spaced dot leaders must not flag")
	}
	toc := "# Contents\n\nAuthor's Note . . . . . . . . . . . . . . . . . . . . . . . . . . . . . ix\n" +
		"Foreword . . . . . . . . . . . . . . . . . . . . . . . . . . . . . . . . . . . . xi\n" +
		"1. A Fountain in the Square . . . . . . . . . . . . . . . . . . . . . . . . . 1\n"
	if Analyze(toc) {
		t.Error("OCR markdown of a spaced-leader contents page must not flag")
	}
	if !Analyze("; . , : ! ? ; . , : ! ? ; . , : ! ? ; . , : ! ? ; . , : ! ? ; . , : ! ? ; . , : ! ? ; . , : ! ? ; . , : ! ? a1") {
		t.Error("spaced mixed punctuation soup must still flag")
	}
}

func shift(s string, by int) string {
	out := []rune(s)
	for i, r := range out {
		if r >= 'a' && r <= 'z' {
			out[i] = rune('a' + (int(r-'a')+by)%26)
		} else if r >= 'A' && r <= 'Z' {
			out[i] = rune('A' + (int(r-'A')+by)%26)
		}
	}
	return string(out)
}

func TestCipherStats(t *testing.T) {
	body := "the quick brown fox jumps over the lazy dog and then it keeps running through several more sentences of ordinary english prose so the sample reaches the required size for statistical confidence in the letter histogram analysis performed here "
	long := ""
	for len(long) < 2200 {
		long += body
	}
	if Analyze(long) {
		t.Error("clean long text must not flag")
	}
	if !Analyze(shift(long[:2200], 9)) {
		t.Error("shifted-alphabet text must flag (cipher stats)")
	}
	starved := strings.ToLower(shift(long[:2200], 2))
	if !Analyze(starved) {
		t.Error("all-lowercase vowel-starved shift must flag via shape cosine")
	}
}

func TestShortSampleNoCipherFlag(t *testing.T) {
	if Analyze(shift("short", 9)) {
		t.Error("under 200 ascii letters the cipher check must not fire")
	}
}
